// Copyright (c) 2026, Omer Sen.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lab

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Environment variables the operator sets. No tool argument can change them.
const (
	EnvKubeconfig   = "NVSENTINEL_MCP_KUBECONFIG"
	EnvAllowChanges = "NVSENTINEL_MCP_ALLOW_CHANGES"
	EnvLabContext   = "NVSENTINEL_MCP_LAB_CONTEXT"

	defaultKubeconfig = ".kube/nvsentinel-lab-mcp.yaml"
	defaultLabContext = "kind-nvsentinel-lab"
)

// Config is what the operator chose for this server.
type Config struct {
	Kubeconfig   string
	LabContext   string
	AllowChanges bool
}

// ConfigFromEnv reads the configuration. Only the exact value "true" allows changes.
func ConfigFromEnv() Config {
	config := Config{
		Kubeconfig:   os.Getenv(EnvKubeconfig),
		LabContext:   os.Getenv(EnvLabContext),
		AllowChanges: os.Getenv(EnvAllowChanges) == "true",
	}

	if config.Kubeconfig == "" {
		if home, err := os.UserHomeDir(); err == nil {
			config.Kubeconfig = home + "/" + defaultKubeconfig
		}
	}

	if config.LabContext == "" {
		config.LabContext = defaultLabContext
	}

	return config
}

// Open loads the one configured kubeconfig and connects to its current context.
// KUBECONFIG and ~/.kube/config are never read. The context is resolved once here,
// so editing the file later cannot redirect this object's calls.
func Open(config Config) (*Lab, error) {
	raw, err := loadKubeconfig(config.Kubeconfig)
	if err != nil {
		return nil, err
	}

	restConfig, err := restConfigFor(raw)
	if err != nil {
		return nil, err
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		slog.Warn("cannot create a Kubernetes client", "error", err)
		return nil, failf("cannot create a Kubernetes client")
	}

	return New(client, &podExecer{client: client, config: restConfig}, raw.CurrentContext, config), nil
}

// privateEnough checks a file's mode. Windows has no such modes: Go reports every file as
// open to all, and access is in ACLs this does not read, so the check cannot apply there.
func privateEnough(mode os.FileMode, goos string) bool {
	return goos == "windows" || mode.Perm()&0o077 == 0
}

func restConfigFor(raw *clientcmdapi.Config) (*rest.Config, error) {
	config, err := clientcmd.NewNonInteractiveClientConfig(
		*raw, raw.CurrentContext, &clientcmd.ConfigOverrides{}, nil,
	).ClientConfig()
	if err != nil {
		slog.Warn("the kubeconfig cannot be used", "error", err)
		return nil, failf("the kubeconfig cannot be used")
	}

	withoutEnvironmentProxy(config, raw)

	return config, nil
}

// loadKubeconfig returns the file's content only if it is a private regular file whose
// current user runs no credential plugin, as a plugin would run a command on this host.
func loadKubeconfig(path string) (*clientcmdapi.Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, failf("no kubeconfig at the configured path")
	}

	if !info.Mode().IsRegular() {
		return nil, failf("the configured kubeconfig is not a regular file")
	}

	if !privateEnough(info.Mode(), runtime.GOOS) {
		return nil, failf("refusing: the kubeconfig is readable by others; chmod 600 it")
	}

	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return nil, failf("the kubeconfig cannot be parsed")
	}

	current, ok := raw.Contexts[raw.CurrentContext]
	if !ok {
		return nil, failf("the kubeconfig has no current context")
	}

	if err := checkUser(raw.AuthInfos[current.AuthInfo]); err != nil {
		return nil, err
	}

	if err := checkCluster(raw.Clusters[current.Cluster]); err != nil {
		return nil, err
	}

	return raw, nil
}

// checkUser allows only credentials held in the file itself. A plugin would run a command,
// a path would read a file with no check on its mode, and impersonation would change who acts.
func checkUser(user *clientcmdapi.AuthInfo) error {
	if user == nil {
		return failf("the kubeconfig's current context has no user")
	}

	switch {
	case user.Exec != nil || user.AuthProvider != nil:
		return failf("refusing: the kubeconfig runs a credential plugin; it can only use a token or certificate")
	case user.TokenFile != "" || user.ClientCertificate != "" || user.ClientKey != "":
		return failf("refusing: the kubeconfig points at credential files; they must be inside it")
	case user.Impersonate != "" || len(user.ImpersonateGroups) > 0 || user.ImpersonateUID != "":
		return failf("refusing: the kubeconfig impersonates another user")
	}

	return nil
}

// checkCluster requires TLS that verifies the server, so the token cannot go to another one.
func checkCluster(cluster *clientcmdapi.Cluster) error {
	if cluster == nil {
		return failf("the kubeconfig's current context has no cluster")
	}

	if cluster.InsecureSkipTLSVerify {
		return failf("refusing: the kubeconfig turns off TLS verification")
	}

	if !strings.HasPrefix(cluster.Server, "https://") {
		return failf("refusing: the kubeconfig's server is not https")
	}

	return nil
}

// withoutEnvironmentProxy stops HTTP(S)_PROXY from sending credentials elsewhere. A
// proxy-url written in the kubeconfig itself is the operator's choice and is kept.
func withoutEnvironmentProxy(config *rest.Config, raw *clientcmdapi.Config) {
	if cluster, ok := raw.Clusters[raw.Contexts[raw.CurrentContext].Cluster]; ok && cluster.ProxyURL != "" {
		return
	}

	config.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
}

func validLabContext(name string) error {
	if !strings.HasPrefix(name, "kind-") {
		return fmt.Errorf("%s must name a kind context, not %q", EnvLabContext, name)
	}

	return nil
}
