package settings

import (
	"encoding/json"

	"github.com/rancher/types/apis/management.cattle.io/v3"
)

var (
	settings = map[string]Setting{}
	provider Provider

	AgentImage                      = newSetting("agent-image", "rancher/rancher-agent:master")
	CACerts                         = newSetting("cacerts", "")
	CLIURLDarwin                    = newSetting("cli-url-darwin", "https://releases.rancher.com/cli/v1.0.0-alpha8/rancher-darwin-amd64-v1.0.0-alpha8.tar.gz")
	CLIURLLinux                     = newSetting("cli-url-linux", "https://releases.rancher.com/cli/v1.0.0-alpha8/rancher-linux-amd64-v1.0.0-alpha8.tar.gz")
	CLIURLWindows                   = newSetting("cli-url-windows", "https://releases.rancher.com/cli/v1.0.0-alpha8/rancher-windows-386-v1.0.0-alpha8.zip")
	EngineInstallURL                = newSetting("engine-install-url", "https://releases.rancher.com/install-docker/17.03.sh")
	EngineISOURL                    = newSetting("engine-iso-url", "https://github.com/boot2docker/boot2docker/releases/download/v17.03.2-ce/boot2docker.iso")
	EngineNewestVersion             = newSetting("engine-newest-version", "v17.12.0")
	EngineSupportedRange            = newSetting("engine-supported-range", "~v1.11.2 || ~v1.12.0 || ~v1.13.0 || ~v17.03.0")
	FirstLogin                      = newSetting("first-login", "true")
	HelmVersion                     = newSetting("helm-version", "dev")
	IngressIPDomain                 = newSetting("ingress-ip-domain", "xip.io")
	InstallUUID                     = newSetting("install-uuid", "")
	KubernetesVersion               = newSetting("k8s-version", v3.DefaultK8s)
	KubernetesVersionToSystemImages = newSetting("k8s-version-to-images", getSystemImages())
	MachineVersion                  = newSetting("machine-version", "dev")
	ServerImage                     = newSetting("server-image", "rancher/rancher")
	ServerURL                       = newSetting("server-url", "")
	ServerVersion                   = newSetting("server-version", "dev")
	SystemDefaultRegistry           = newSetting("system-default-registry", "")
	SystemNamespaces                = newSetting("system-namespaces", "kube-system,kube-public,cattle-system,cattle-alerting,cattle-logging,cattle-pipeline")
	TelemetryOpt                    = newSetting("telemetry-opt", "")
	UIFeedBackForm                  = newSetting("ui-feedback-form", "")
	UIIndex                         = newSetting("ui-index", "https://releases.rancher.com/ui/latest2/index.html")
	UIPath                          = newSetting("ui-path", "")
	UIPL                            = newSetting("ui-pl", "rancher")
	WhitelistDomain                 = newSetting("whitelist-domain", "forums.rancher.com")
	RDNSServerBaseURL               = newSetting("rdns-base-url", "http://54.215.121.160:9333/v1")
	//RDNSServerBaseURL               = newSetting("rdns-base-url", "http://api.lb.rancher.cloud/v1")
)

type Provider interface {
	Get(name string) string
	Set(name, value string) error
	SetIfUnset(name, value string) error
	SetAll(settings map[string]Setting) error
}

type Setting struct {
	Name     string
	Default  string
	ReadOnly bool
}

func (s Setting) SetIfUnset(value string) error {
	if provider == nil {
		return s.Set(value)
	}
	return provider.SetIfUnset(s.Name, value)
}

func (s Setting) Set(value string) error {
	if provider == nil {
		s, ok := settings[s.Name]
		if ok {
			s.Default = value
			settings[s.Name] = s
		}
	} else {
		return provider.Set(s.Name, value)
	}
	return nil
}

func (s Setting) Get() string {
	if provider == nil {
		s := settings[s.Name]
		return s.Default
	}
	return provider.Get(s.Name)
}

func SetProvider(p Provider) error {
	if err := p.SetAll(settings); err != nil {
		return err
	}
	provider = p
	return nil
}

func newSetting(name, def string) Setting {
	s := Setting{
		Name:    name,
		Default: def,
	}
	settings[s.Name] = s
	return s
}

func getSystemImages() string {
	versionToSystemImages := v3.K8sVersionToRKESystemImages

	data, err := json.Marshal(versionToSystemImages)
	if err != nil {
		return ""
	}
	return string(data)
}
