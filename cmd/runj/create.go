package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"go.sbk.wtf/runj/jail"
	"go.sbk.wtf/runj/oci"
	"go.sbk.wtf/runj/runtimespec"
	"go.sbk.wtf/runj/state"
        "github.com/containerd/containerd/mount"
        "github.com/pkg/errors"
	"github.com/gizahNL/gojail"


	"github.com/spf13/cobra"
)

// createCommand implements the OCI "create" command
//
// create <container-id> <path-to-bundle>
//
// This operation MUST generate an error if it is not provided a path to the
// bundle and the container ID to associate with the container. If the ID
// provided is not unique across all containers within the scope of the runtime,
// or is not valid in any other way, the implementation MUST generate an error
// and a new container MUST NOT be created. This operation MUST create
// a new container.
//
// All of the properties configured in config.json except for process MUST be
// applied. process.args MUST NOT be applied until triggered by the start
// operation. The remaining process properties MAY be applied by this operation.
// If the runtime cannot apply a property as specified in the configuration, it
// MUST generate an error and a new container MUST NOT be created.
//
// The runtime MAY validate config.json against this spec, either generically or
// with respect to the local system capabilities, before creating the container
// (step 2). Runtime callers who are interested in pre-create validation can run
// bundle-validation tools before invoking the create operation.
//
// Any changes made to the config.json file after this operation will not have
// an effect on the container.
//
// runc's implementation of `create` hooks up the container process's STDIO to
// the same STDIO streams used for the invocation  of `runc create`.  Because
// integrations on top of runc expect this behavior, runj copies that at the
// expense of more complication in the codebase.
func createCommand() *cobra.Command {
	create := &cobra.Command{
		Use:   "create <container-id> <path-to-bundle>",
		Short: "Create a new container with given ID and bundle",
		Long: `Create a new container with given ID and bundle.  IDs must be unique.

The create command creates an instance of a container for a bundle. The bundle
is a directory with a specification file named "config.json" and a root
filesystem.

The specification file includes an args parameter. The args parameter is used
to specify command(s) that get run when the container is started. To change the
command(s) that get executed on start, edit the args parameter of the spec.`,
		Args: cobra.ExactArgs(2),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			bundle := args[1]
			bundleConfig := filepath.Join(bundle, oci.ConfigFileName)
			fInfo, err := os.Stat(bundleConfig)
			if err != nil {
				return err
			}
			if fInfo.Mode()&os.ModeType != 0 {
				return fmt.Errorf("%q should be a regular file", bundleConfig)
			}
			return nil
		},
	}
	consoleSocket := create.Flags().String(
		"console-socket",
		"",
		`path to an AF_UNIX socket which will receive a
file descriptor referencing the master end of
the console's pseudoterminal`)
	create.RunE = func(cmd *cobra.Command, args []string) (err error) {
		disableUsage(cmd)
		id := args[0]
		bundle := args[1]
		var s *state.State
		s, err = state.Create(id, bundle)
		if err != nil {
			return err
		}
		defer func() {
			if err == nil {
				s.Status = state.StatusCreated
				err = s.Save()
			} else {
				state.Remove(id)
			}
		}()
		err = oci.StoreConfig(id, bundle)
		if err != nil {
			return err
		}
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
		// setup mounts
		if ociConfig.Mounts != nil {
			for _, sm := range ociConfig.Mounts {
				m := &mount.Mount{
					Type: sm.Type,
					Source: sm.Source,
					Options: sm.Options,
				}
				if sm.Destination == "/dev" {
					m.Options = append(m.Options, "ruleset=5")
				}
				target := filepath.Join(rootPath, sm.Destination)
				if err := m.Mount(target); err != nil {
					return errors.Wrapf(err, "failed to mount %v", m)
				}
				//unmount on create failure
				defer func() {
					if err != nil {
						mount.UnmountAll(target, 0)
					}
				}()
			}
		}


		// console socket validation
		if ociConfig.Process.Terminal {
			if *consoleSocket == "" {
				return errors.New("console-socket is required when Process.Terminal is true")
			}
			if socketStat, err := os.Stat(*consoleSocket); err != nil {
				return fmt.Errorf("failed to stat console socket %q: %w", *consoleSocket, err)
			} else {
				if socketStat.Mode()&os.ModeSocket != os.ModeSocket {
					return fmt.Errorf("console-socket %q is not a socket", *consoleSocket)
				}
			}
		} else if *consoleSocket != "" {
			return errors.New("console-socket provided but Process.Terminal is false")
		}
		jailconfig := make(map[string]interface{})
		jailconfig["name"] = id
		jailconfig["path"] = rootPath
		jailconfig["persist"] = true
		jailconfig["allow.raw_sockets"] = true
		jailconfig["host.hostname"] = ociConfig.Hostname
		jailconfig["ip4"] = "inherit"
		jailconfig["ip4.saddrsel"] = false
		jailconfig["ip6"] = "inherit"
		jailconfig["ip6.saddrsel"] = false

		var Jail gojail.Jail
		if ociConfig.Freebsd != nil && ociConfig.Freebsd.JailOptions.Parent != "" {
			parent, err := gojail.JailGetByName(ociConfig.Freebsd.JailOptions.Parent)
			if err != nil {
				return err
			}
			Jail, err = parent.CreateChildJail(jailconfig)
			if err != nil {
				return err
			}
		} else {
			Jail, err = gojail.JailCreate(jailconfig)
		}
		if err != nil {
			return fmt.Errorf("failed creating jail: %w", err)
		}
		defer func() {
			if err != nil {
				Jail.Destroy()
			}
		}()
		s.JID = int(Jail.ID())

		if ociConfig.Hooks != nil && ociConfig.Hooks.CreateRuntime != nil {
			for _, hook:= range ociConfig.Hooks.CreateRuntime {
				if err := exec.Command(hook.Path, hook.Args ...).Run(); err != nil {
					return fmt.Errorf("failed executing createruntime hook: %+v", hook)
				}
			}
		}
		if ociConfig.Hooks != nil && ociConfig.Hooks.CreateRuntime != nil {
			for _, hook := range ociConfig.Hooks.CreateContainer {
				args := []string{ id, hook.Path,}
				args = append(args, hook.Args ...)
				if err := exec.Command("/usr/sbin/jexec", args ...).Run(); err != nil {
					return fmt.Errorf("failed executing createcontainer hook: %+v", hook)
				}
			}
		}

		// Setup and start the "runj-entrypoint" helper program in order to
		// get the container STDIO hooked up properly.
		var entrypoint *exec.Cmd
		entrypoint, err = jail.SetupEntrypoint(id, true, ociConfig.Process.Cwd, ociConfig.Process.Args, ociConfig.Process.Env, *consoleSocket)
		if err != nil {
			return err
		}
		// the runj-entrypoint pid will become the container process's pid
		// through a series of exec(2) calls
		s.PID = entrypoint.Process.Pid
		return nil
	}
	return create
}
