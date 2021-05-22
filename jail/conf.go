package jail

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"go.sbk.wtf/runj/runtimespec"
	"go.sbk.wtf/runj/state"
)

const (
	confName       = "jail.conf"
	configTemplate = `{{ .Name }} {
  allow.raw_sockets;
  allow.mlock;
  sysvmsg = new;
  sysvsem = new;
  sysvshm = new;

  path = "{{ .Root }}";
  vnet;
  persist;
}
`
)

func CreateConfig(id, root string, mounts []runtimespec.Mount) (string, error) {
	config, err := renderConfig(id, root)
	fstab := renderFstab(root, mounts)
	if err != nil {
		return "", err
	}
	confPath := ConfPath(id)
	fstabPath := FstabPath(id)
	confFile, err := os.OpenFile(confPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("jail: config should not already exist: %w", err)
	}
	fstabFile, err := os.OpenFile(fstabPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", fmt.Errorf("jail: fstab should not already exist: %w", err)
	}
	defer func() {
		confFile.Close()
		if err != nil {
			os.Remove(confFile.Name())
		}
		fstabFile.Close()
		if err != nil {
			os.Remove(fstabFile.Name())
		}
	}()
	_, err = confFile.Write([]byte(config))
	if err != nil {
		return "", err
	}
	_, err = fstabFile.Write([]byte(fstab))
	if err != nil {
		return "", err
	}
	return confFile.Name(), nil
}

func ConfPath(id string) string {
	return filepath.Join(state.Dir(id), confName)
}

func FstabPath(id string) string {
	return filepath.Join(state.Dir(id), "fstab")
}

func renderFstab(root string, mounts []runtimespec.Mount) (fstab string) {
	for _, mount := range mounts {
		if mount.Type == "mqueue" {
			continue
		}
		fstab += mount.Source
		fstab += "\t"
		fstab += filepath.Join(root, mount.Destination)
		fstab += "\t"
		fstab += mount.Type
		fstab += "\t"

		fstab += "rw,late"
		if mount.Destination == "/dev" {
			fstab+=",ruleset=5"
		} else if len(mount.Options) > 0 {
			for _, option := range mount.Options {
				if option == "strictatime" {
					continue
				}
				fstab += ","
				fstab += option
			}
		}
		fstab += "\t0 0\n"
	}
	return fstab
}

func renderConfig(id, root string) (string, error) {
	config, err := template.New("config").Parse(configTemplate)
	if err != nil {
		return "", err
	}
	buf := bytes.Buffer{}
	config.Execute(&buf, struct {
		Name  string
		Root  string
		Fstab string
	}{
		Name:  id,
		Root:  root,
		Fstab: filepath.Join(state.Dir(id), "fstab"),
	})
	return buf.String(), nil
}
