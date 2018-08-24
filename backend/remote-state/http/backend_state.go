package http

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform/backend"
	"github.com/hashicorp/terraform/state"
	"github.com/hashicorp/terraform/state/remote"
	"github.com/hashicorp/terraform/terraform"
)

const (
	stateFileSuffix = ".tfstate"
	lockFileSuffix  = ".tflock"
)

// States get a list of all states.
func (b *Backend) States() ([]string, error) {
	var result []string
	client := &RemoteClient{
		client:   b.client,
		address:  b.address,
		username: b.username,
		password: b.password,
	}

	resp, err := client.Get()
	if err != nil {
		return nil, err
	}
	// Read in the body
	// Get all the data sent by REST
	buff := string(resp.Data)
	buff = strings.Replace(buff, ",", " ", -1)
	buffSlice := strings.Fields(buff)
	sort.Strings(buffSlice)
	for _, el := range buffSlice {
		// iterate on response and make sure we only get .tfstate files
		// This should be implemented in REST API but just to be safe.
		fName := filepath.Base(el)
		extName := filepath.Ext(el)
		bname := fName[:len(fName)-len(extName)]
		if extName == stateFileSuffix {
			result = append(result, bname)
		}
	}

	sort.Strings(result[1:])
	if sort.SearchStrings(result, backend.DefaultStateName) == 0 {
		result = append(result, backend.DefaultStateName)
	}
	return result, nil
}

// DeleteState deletes a state file
func (b *Backend) DeleteState(name string) error {
	if name == backend.DefaultStateName || name == "" {
		return fmt.Errorf("can't delete default state")
	}
	client, err := b.remoteClient(name)
	if err != nil {
		return err
	}

	return client.Delete()
}

// get a remote client configured for this state
func (b *Backend) remoteClient(name string) (*RemoteClient, error) {
	if name == "" {
		return nil, errors.New("missing state name")
	}
	client := &RemoteClient{
		client:        b.client,
		address:       b.statePath(name),
		updateMethod:  b.updateMethod,
		lockAddress:   b.lockPath(name),
		unlockAddress: b.lockPath(name),
		lockMethod:    b.lockMethod,
		unlockMethod:  b.unlockMethod,
		username:      b.username,
		password:      b.password,
	}
	return client, nil
}

// State reads the state file
func (b *Backend) State(name string) (state.State, error) {
	client, err := b.remoteClient(name)
	if err != nil {
		return nil, err
	}

	stateMgr := &remote.State{Client: client}

	// Grab the value
	if err := stateMgr.RefreshState(); err != nil {
		return nil, err
	}

	// If we have no state, we have to create an empty state
	if v := stateMgr.State(); v == nil {
		// take a lock on this state while we write it
		lockInfo := state.NewLockInfo()
		lockInfo.Operation = "init"
		lockID, err := client.Lock(lockInfo)

		if err != nil {
			return nil, fmt.Errorf("failed to lock http state: %s", err)
		}

		// Local helper function so we can call it multiple places
		lockUnlock := func(parent error) error {
			if err := stateMgr.Unlock(lockID); err != nil {
				return fmt.Errorf(strings.TrimSpace(errStateUnlock), lockID, err)
			}
			return parent
		}

		if err := stateMgr.WriteState(terraform.NewState()); err != nil {
			err = lockUnlock(err)
			return nil, err
		}
		if err := stateMgr.PersistState(); err != nil {
			err = lockUnlock(err)
			return nil, err
		}

		// Unlock, the state should now be initialized
		if err := lockUnlock(nil); err != nil {
			return nil, err
		}
	}
	return stateMgr, nil
}

// Construct the path of the state file based on named state
func (b *Backend) statePath(name string) string {
	paths := []string{b.address, "/", name, stateFileSuffix}
	var buf bytes.Buffer
	for _, p := range paths {
		buf.WriteString(p)
	}
	path := buf.String()

	return path
}

// Construct the path of the lock file based on named state
func (b *Backend) lockPath(name string) string {
	paths := []string{b.address, "/", name, lockFileSuffix}
	var buf bytes.Buffer
	for _, p := range paths {
		buf.WriteString(p)
	}
	path := buf.String()

	return path
}

const errStateUnlock = `
Error unlocking http state. Lock ID: %s

Error: %s

You may have to force-unlock this state in order to use it again.
`
