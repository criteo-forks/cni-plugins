package main

import (
	"fmt"
	"net"
	"time"

	"github.com/vishvananda/netlink"

	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/netlinksafe"
)

// findVRF finds a VRF link with the provided name.
func findVRF(name string) (*netlink.Vrf, error) {
	link, err := netlinksafe.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("VRF %s not found", name)
	}
	vrf, ok := link.(*netlink.Vrf)
	if !ok {
		return nil, fmt.Errorf("Netlink %s is not a VRF", name)
	}
	return vrf, nil
}

// findHostInterface finds the host interface with the provided name.
// Use the sandbox field to distinguish interface on host or netns
// See doc in https://www.cni.dev/docs/spec/#add-success for more info
func findHostInterface(interfaces []*current.Interface) (*current.Interface, error) {
	for _, intf := range interfaces {
		if intf.Sandbox == "" {
			return intf, nil
		}
	}
	return nil, fmt.Errorf("Cannot find the corresponding host interface")
}

// assignedInterfaces returns the list of interfaces associated to the given vrf.
func assignedInterfaces(vrf *netlink.Vrf) ([]netlink.Link, error) {
	links, err := netlinksafe.LinkList()
	if err != nil {
		return nil, fmt.Errorf("getAssignedInterfaces: Failed to find links %v", err)
	}
	res := make([]netlink.Link, 0)
	for _, l := range links {
		if l.Attrs().MasterIndex == vrf.Index {
			res = append(res, l)
		}
	}
	return res, nil
}

// addInterface adds the given interface to the VRF
func addInterface(vrf *netlink.Vrf, intf string) error {
	i, err := netlinksafe.LinkByName(intf)
	if err != nil {
		return fmt.Errorf("could not get link by name %s", intf)
	}

	if i.Attrs().MasterIndex != 0 {
		master, err := netlink.LinkByIndex(i.Attrs().MasterIndex)
		if err != nil {
			return fmt.Errorf("interface %s has already a master set, could not retrieve the name: %v", intf, err)
		}
		return fmt.Errorf("interface %s has already a master set: %s", intf, master.Attrs().Name)
	}

	// Global IPV6 addresses are not maintained unless
	// sysctl -w net.ipv6.conf.all.keep_addr_on_down=1 is called
	// so we save it, and restore it back.
	beforeAddresses, err := getGlobalAddresses(i, netlink.FAMILY_V6)
	if err != nil {
		return fmt.Errorf("failed getting global ipv6 addresses before slaving interface: %w", err)
	}

	// Save all routes that are not local and connected, before setting master,
	// because otherwise those routes will be deleted after interface is moved.
	filter := &netlink.Route{
		LinkIndex: i.Attrs().Index,
		Scope:     netlink.SCOPE_UNIVERSE, // Exclude local and connected routes
	}
	filterMask := netlink.RT_FILTER_OIF | netlink.RT_FILTER_SCOPE // Filter based on link index and scope
	r, err := netlinksafe.RouteListFiltered(netlink.FAMILY_ALL, filter, filterMask)
	if err != nil {
		return fmt.Errorf("failed getting all routes for %s", intf)
	}

	// Filter out connected IPV6 routes
	globalRoutes := make([]netlink.Route, 0, len(r))
	for _, route := range r {
		if route.Src != nil {
			globalRoutes = append(globalRoutes, route)
		}
	}

	err = netlink.LinkSetMaster(i, vrf)
	if err != nil {
		return fmt.Errorf("could not set vrf %s as master of %s: %v", vrf.Name, intf, err)
	}

	// Used to identify which global IPV6 addresses are missing
	afterAddresses, err := getGlobalAddresses(i, netlink.FAMILY_V6)
	if err != nil {
		return fmt.Errorf("failed getting global ipv6 addresses after slaving interface: %w", err)
	}

	// Since keeping the ipv6 address depends on net.ipv6.conf.all.keep_addr_on_down ,
	// we check if the new interface does not have them and in case we restore them.
CONTINUE:
	for _, toFind := range beforeAddresses {
		for _, current := range afterAddresses {
			if toFind.Equal(current) {
				continue CONTINUE
			}
		}
		// Not found, re-adding it
		err = netlink.AddrAdd(i, &toFind)
		if err != nil {
			return fmt.Errorf("could not restore address %s to %s @ %s: %v", toFind, intf, vrf.Name, err)
		}

		// Waits for global IPV6 addresses to be added by the kernel.
		backoffBase := 10 * time.Millisecond
		maxRetries := 8
		for retryCount := 0; retryCount <= maxRetries; retryCount++ {
			routesVRFTable, err := netlinksafe.RouteListFiltered(
				netlink.FAMILY_ALL,
				&netlink.Route{
					Dst: &net.IPNet{
						IP:   toFind.IP,
						Mask: net.IPMask{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
					},
					Table:     int(vrf.Table),
					LinkIndex: i.Attrs().Index,
				},
				netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE|netlink.RT_FILTER_DST,
			)
			if err != nil {
				return fmt.Errorf("failed getting routes for %s table %d for dst %s: %v", intf, vrf.Table, toFind.IPNet.String(), err)
			}

			if len(routesVRFTable) >= 1 {
				break
			}

			if retryCount == maxRetries {
				return fmt.Errorf("failed getting local/host addresses for %s in table %d with dst %s", intf, vrf.Table, toFind.IPNet.String())
			}

			// Exponential backoff - 10ms, 20m, 40ms, 80ms, 160ms, 320ms, 640ms, 1280ms
			// Approx 2,5 seconds total
			time.Sleep(backoffBase * time.Duration(1<<retryCount))
		}
	}

	// Apply all saved routes for the interface that was moved to the VRF
	for _, route := range globalRoutes {
		r := route
		// Modify original table to vrf one,
		r.Table = int(vrf.Table)
		// equivalent of 'ip route replace <address> table <int>'.
		err = netlink.RouteReplace(&r)
		if err != nil {
			return fmt.Errorf("could not add route '%s': %v", r, err)
		}
	}

	return nil
}

// getGlobalAddresses returns the global addresses of the given interface
func getGlobalAddresses(link netlink.Link, family int) ([]netlink.Addr, error) {
	addresses, err := netlinksafe.AddrList(link, family)
	if err != nil {
		return nil, fmt.Errorf("failed getting list of IP addresses for %s: %w", link.Attrs().Name, err)
	}

	globalAddresses := make([]netlink.Addr, 0, len(addresses))
	for _, addr := range addresses {
		if addr.Scope == int(netlink.SCOPE_UNIVERSE) {
			globalAddresses = append(globalAddresses, addr)
		}
	}

	return globalAddresses, nil
}

func resetMaster(interfaceName string) error {
	intf, err := netlinksafe.LinkByName(interfaceName)
	if err != nil {
		return fmt.Errorf("resetMaster: could not get link by name %s", interfaceName)
	}
	err = netlink.LinkSetNoMaster(intf)
	if err != nil {
		return fmt.Errorf("resetMaster: could not reset master to %s", interfaceName)
	}
	return nil
}
