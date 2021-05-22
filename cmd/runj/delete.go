package main

import (
	"errors"
	"fmt"
	"os"
        "path/filepath"

	"go.sbk.wtf/runj/state"

	"go.sbk.wtf/runj/oci"
	"go.sbk.wtf/runj/runtimespec"
        "github.com/containerd/containerd/mount"

	"go.sbk.wtf/runj/jail"

	"github.com/spf13/cobra"
)

// deleteContainer implements the OCI "delete" command
//
// delete <container-id>
//
// This operation MUST generate an error if it is not provided the container ID.
// Attempting to delete a container that is not stopped MUST have no effect on
// the container and MUST generate an error. Deleting a container MUST delete
// the resources that were created during the create step. Note that resources
// associated with the container, but not created by this container, MUST NOT be
// deleted. Once a container is deleted its ID MAY be used by a subsequent
// container.
func deleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <container-id>",
		Short: "Delete a container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			disableUsage(cmd)
			id := args[0]
			running, err := jail.IsRunning(cmd.Context(), id, 0)
			if err != nil {
				return fmt.Errorf("delete: failed to determine if jail is running: %w", err)
			}
			if running {
				return fmt.Errorf("delete: jail %s is not stopped", id)
			}
			err = jail.CleanupEntrypoint(id)
			if err != nil {
				return fmt.Errorf("delete: failed to find entrypoint process: %w", err)
			}
			confPath := jail.ConfPath(id)
			if _, err := os.Stat(confPath); err != nil {
				return errors.New("invalid jail id provided")
			}
			err = jail.DestroyJail(cmd.Context(), confPath, id)
			if err != nil {
				return err
			}
			runjstate, err := state.Load(id)
			if err != nil {
				return err
			}
			bundle := runjstate.Bundle
	                var ociConfig *runtimespec.Spec
	                ociConfig, err = oci.LoadConfig(id)
	                if err != nil {
		                return err
	                }
	                rootPath := filepath.Join(bundle, "root")
	                if ociConfig != nil && ociConfig.Root != nil && ociConfig.Root.Path != "" {
				rootPath = ociConfig.Root.Path
	                        if rootPath[0] != filepath.Separator {
		                        rootPath = filepath.Join(bundle, rootPath)
	                        }
	                }
			var failedUnmount error
			if ociConfig.Mounts != nil {
				for _, sm := range ociConfig.Mounts {
					target := filepath.Join(rootPath, sm.Destination)
					if err := mount.UnmountAll(target, 0); err != nil {
						fmt.Printf("failed to unmount %s: %+v\n", target, err)
						failedUnmount = err
					}
				}
			}
			if failedUnmount != nil {
				return failedUnmount
			}




			return state.Remove(id)
		},
	}
}
