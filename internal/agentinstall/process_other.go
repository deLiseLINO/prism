//go:build !unix

package agentinstall

import "errors"

func processGroupGone(group int) (bool, error) {
	return false, errors.New("process group cleanup confirmation is unavailable on this platform")
}
