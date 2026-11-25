//go:build !unix

package collie

import "os/exec"

func configureCLIProcGroup(cmd *exec.Cmd) {}
