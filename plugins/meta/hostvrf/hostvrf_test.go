package main

import (
	"fmt"
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/vishvananda/netlink"

	"github.com/containernetworking/cni/pkg/skel"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/netlinksafe"
	"github.com/containernetworking/plugins/pkg/testutils"
)

var _ = Describe("All tests ordered", Ordered, func() {
	const (
		IF0Name  = "dummy0"
		TargetNS = "targetNS"
		VRF0Name = "vrf0"
		Subnet   = "10.0.0.2/24"
	)
	Describe("vrf plugin CMD ADD", func() {
		var vrf0 *netlink.Vrf

		BeforeEach(func() {
			var err error
			Expect(createDummyInterface(IF0Name)).NotTo(HaveOccurred())
			vrf0, err = createVRF(VRF0Name)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			Expect(deleteInterface(IF0Name)).NotTo(HaveOccurred())
			err := netlink.LinkDel(vrf0)
			Expect(err).To(Succeed())
		})

		It("CMD ADD call succeed + Host Interface enslaved", func() {
			conf := configFor("test", IF0Name, VRF0Name, Subnet)

			args := &skel.CmdArgs{
				ContainerID: "dummy",
				Netns:       TargetNS,
				IfName:      IF0Name,
				StdinData:   conf,
			}

			r, _, err := testutils.CmdAddWithArgs(args, func() error {
				return cmdAdd(args)
			})
			Expect(err).NotTo(HaveOccurred())

			// Check cmdAddResult
			result, err := current.GetResult(r)
			Expect(err).NotTo(HaveOccurred())

			Expect(result.Interfaces).To(HaveLen(2))
			Expect(result.Interfaces[0].Name).To(Equal(IF0Name))
			Expect(result.IPs).To(HaveLen(1))
			Expect(result.IPs[0].Address.String()).To(Equal(Subnet))

			// Check Interface and VRF setup
			checkInterfaceOnVRF(VRF0Name, IF0Name)
		})
	})

	Describe("vrf plugin CMD CHECK/DEL", func() {
		var vrf0 *netlink.Vrf
		var err error

		conf := configFor("test", IF0Name, VRF0Name, Subnet)
		args := &skel.CmdArgs{
			ContainerID: "dummy",
			Netns:       TargetNS,
			IfName:      IF0Name,
			StdinData:   conf,
		}
		It("CMD ADD call passes", func() {
			By("Creating the dummy interface", func() {
				Expect(createDummyInterface(IF0Name)).NotTo(HaveOccurred())
			})
			By("Creating the VRF", func() {
				vrf0, err = createVRF(VRF0Name)
				Expect(err).NotTo(HaveOccurred())
			})
			_, _, err := testutils.CmdAddWithArgs(args, func() error {
				return cmdAdd(args)
			})
			Expect(err).NotTo(HaveOccurred())
		})
		It("CMD CHECK call passes", func() {
			err := testutils.CmdCheckWithArgs(args, func() error {
				return cmdCheck(args)
			})
			Expect(err).NotTo(HaveOccurred())
		})
		It("CMD DEL call passes", func() {
			err = testutils.CmdDelWithArgs(args, func() error {
				return cmdDel(args)
			})
			Expect(err).NotTo(HaveOccurred())
			checkInterfaceNotOnVRF(VRF0Name, IF0Name)

			By("Cleaning Interface + VRF", func() {
				Expect(deleteInterface(IF0Name)).NotTo(HaveOccurred())
				err = netlink.LinkDel(vrf0)
				Expect(err).To(Succeed())
			})
		})
	})
})

func configFor(name, intf, vrf, ip string) []byte {
	conf := fmt.Sprintf(`{
        "name": "%s",
        "type": "hostvrf",
        "cniVersion": "0.3.1",
        "vrfName": "%s",
        "prevResult": {
            "interfaces": [
                {"name": "%s"},
                {"name": "eth0", "sandbox":"netns"}
            ],
            "ips": [
                {
                    "version": "4",
                    "address": "%s",
                    "gateway": "10.0.0.1",
                    "interface": 1
                }
            ]
        }
    }`, name, vrf, intf, ip)
	return []byte(conf)
}

// createVRF creates a new VRF and sets it up.
func createVRF(name string) (*netlink.Vrf, error) {
	links, err := netlinksafe.LinkList()
	if err != nil {
		return nil, fmt.Errorf("createVRF: Failed to find links %v", err)
	}

	tableID, err := findFreeRoutingTableID(links)
	if err != nil {
		return nil, err
	}

	linkAttrs := netlink.NewLinkAttrs()
	linkAttrs.Name = name
	vrf := &netlink.Vrf{
		LinkAttrs: linkAttrs,
		Table:     tableID,
	}

	err = netlink.LinkAdd(vrf)
	if err != nil {
		return nil, fmt.Errorf("could not add VRF %s: %v", name, err)
	}
	err = netlink.LinkSetUp(vrf)
	if err != nil {
		return nil, fmt.Errorf("could not set link up for VRF %s: %v", name, err)
	}

	return vrf, nil
}

func findFreeRoutingTableID(links []netlink.Link) (uint32, error) {
	takenTables := make(map[uint32]struct{}, len(links))
	for _, l := range links {
		if vrf, ok := l.(*netlink.Vrf); ok {
			takenTables[vrf.Table] = struct{}{}
		}
	}

	for res := uint32(1); res < math.MaxUint32; res++ {
		if _, ok := takenTables[res]; !ok {
			return res, nil
		}
	}
	return 0, fmt.Errorf("findFreeRoutingTableID: Failed to find an available routing id")
}

func createDummyInterface(name string) error {
	dummy := &netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{
			Name: name,
		},
	}

	// Add the dummy interface
	if err := netlink.LinkAdd(dummy); err != nil {
		return fmt.Errorf("failed to add dummy interface: %v", err)
	}
	return nil
}

func deleteInterface(name string) error {
	link, err := netlinksafe.LinkByName(name)
	if err != nil {
		return fmt.Errorf("interface %s not found: %v", name, err)
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete interface %s: %v", name, err)
	}
	return nil
}

func checkInterfaceOnVRF(vrfName, intfName string) {
	vrf, err := netlinksafe.LinkByName(vrfName)
	Expect(err).NotTo(HaveOccurred())
	Expect(vrf).To(BeAssignableToTypeOf(&netlink.Vrf{}))

	link, err := netlinksafe.LinkByName(intfName)
	Expect(err).NotTo(HaveOccurred())
	masterIndx := link.Attrs().MasterIndex
	master, err := netlink.LinkByIndex(masterIndx)
	Expect(err).NotTo(HaveOccurred())
	Expect(master.Attrs().Name).To(Equal(vrfName))
}

func checkInterfaceNotOnVRF(vrfName, intfName string) {
	vrf, err := netlinksafe.LinkByName(vrfName)
	Expect(err).NotTo(HaveOccurred())
	Expect(vrf).To(BeAssignableToTypeOf(&netlink.Vrf{}))

	link, err := netlinksafe.LinkByName(intfName)
	Expect(err).NotTo(HaveOccurred())
	Expect(link.Attrs().MasterIndex).To(Equal(0))
}
