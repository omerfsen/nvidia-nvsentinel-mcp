// kubectl for the image: upstream's own command tree, built with the dependencies
// in go.mod, without the cloud auth plugins (the server refuses plugins anyway).
package main

import (
	"k8s.io/component-base/cli"
	"k8s.io/kubectl/pkg/cmd"
	"k8s.io/kubectl/pkg/cmd/util"
)

func main() {
	command := cmd.NewDefaultKubectlCommand()
	if err := cli.RunNoErrOutput(command); err != nil {
		util.CheckErr(err)
	}
}
