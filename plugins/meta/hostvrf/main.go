package main

import (
	"encoding/json"
	"fmt"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	bv "github.com/containernetworking/plugins/pkg/utils/buildversion"
)

// VRFNetConf represents the vrf configuration.
type VRFNetConf struct {
	types.NetConf

	// VRFName is the name of the vrf to add the interface to.
	VRFName string `json:"vrfname"`
}

func main() {
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add:   cmdAdd,
		Check: cmdCheck,
		Del:   cmdDel,
		/* FIXME GC */
		/* FIXME Status */
	}, version.VersionsStartingFrom("0.3.1"), bv.BuildString("hostvrf"))
}

func cmdAdd(args *skel.CmdArgs) error {
	conf, result, err := parseConf(args.StdinData)
	if err != nil {
		return err
	}

	if conf.PrevResult == nil {
		return fmt.Errorf("missing prevResult from earlier plugin")
	}

	vrf, err := findVRF(conf.VRFName)
	if err != nil {
		return err
	}

	// Use the sandbox field to distinguish interface on host or netns
	hostInterface, err := findHostInterface(result.Interfaces)
	if err != nil {
		return err
	}

	err = addInterface(vrf, hostInterface.Name)
	if err != nil {
		return err
	}

	if result == nil {
		result = &current.Result{}
	}

	return types.PrintResult(result, conf.CNIVersion)
}

func cmdDel(args *skel.CmdArgs) error {
	conf, result, err := parseConf(args.StdinData)
	if err != nil {
		return err
	}

	_, err = findVRF(conf.VRFName)
	if err != nil {
		return err
	}

	// Use the sandbox field to distinguish interface on host or netns
	hostInterface, err := findHostInterface(result.Interfaces)
	if err != nil {
		return err
	}

	err = resetMaster(hostInterface.Name)
	if err != nil {
		return err
	}

	return nil
}

func cmdCheck(args *skel.CmdArgs) error {
	conf, result, err := parseConf(args.StdinData)
	if err != nil {
		return err
	}

	// Ensure we have previous result.
	if conf.PrevResult == nil {
		return fmt.Errorf("missing prevResult from earlier plugin")
	}

	vrf, err := findVRF(conf.VRFName)
	if err != nil {
		return err
	}
	vrfInterfaces, err := assignedInterfaces(vrf)
	if err != nil {
		return err
	}

	found := false
	for _, intf := range vrfInterfaces {
		if intf.Attrs().Name == result.Interfaces[0].Name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("failed to find %s associated to vrf %s", args.IfName, conf.VRFName)
	}

	return nil
}

func parseConf(data []byte) (*VRFNetConf, *current.Result, error) {
	conf := VRFNetConf{}
	if err := json.Unmarshal(data, &conf); err != nil {
		return nil, nil, fmt.Errorf("failed to load netconf: %v", err)
	}

	if conf.VRFName == "" {
		return nil, nil, fmt.Errorf("configuration is expected to have a valid vrf name")
	}

	if conf.RawPrevResult == nil {
		// return early if there was no previous result, which is allowed for DEL calls
		return &conf, &current.Result{}, nil
	}

	// Parse previous result.
	var result *current.Result
	var err error
	if err = version.ParsePrevResult(&conf.NetConf); err != nil {
		return nil, nil, fmt.Errorf("could not parse prevResult: %v", err)
	}

	result, err = current.NewResultFromResult(conf.PrevResult)
	if err != nil {
		return nil, nil, fmt.Errorf("could not convert result to current version: %v", err)
	}

	return &conf, result, nil
}
