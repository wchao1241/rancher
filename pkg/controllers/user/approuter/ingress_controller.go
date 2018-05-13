package approuter

import (
	"fmt"

	"github.com/rancher/rancher/pkg/settings"
	"github.com/rancher/types/apis/core/v1"
	"github.com/rancher/types/apis/extensions/v1beta1"
	"github.com/sirupsen/logrus"
	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	annotationHostname           = "rdns.cattle.io/hostname"
	annotationIngressClass       = "kubernetes.io/ingress.class"
	ingressClassNginx            = "nginx"
	defaultNginxIngressNamespace = "ingress-nginx"
	refreshIngressHostnameKey    = "_refreshRDNSHostname_"
)

type Controller struct {
	ingressInterface       v1beta1.IngressInterface
	ingressLister          v1beta1.IngressLister
	managementSecretLister v1.SecretLister
	clusterName            string
}

func (c *Controller) sync(key string, obj *extensionsv1beta1.Ingress) error {
	//will not do clean if domain setting is cleaned
	serverURL := settings.BaseRDNSServerURL.Get()
	if serverURL == "" {
		logrus.Warnf("settings.baseRDNSServerURL is not set, dns name might not be reachable")
	}

	_, fqdn, err := c.getSecret()

	logrus.Debugf("=========fqdn========%s", fqdn)

	if k8serrors.IsNotFound(err) {
		return nil
	}

	logrus.Debugf("========key========%s======refreshIngressHostnameKey====%s", key, refreshIngressHostnameKey)

	if key == refreshIngressHostnameKey {
		logrus.Debugf("====aaaaaa====key========%s", key)
		return c.refreshAll(fqdn)
	}
	logrus.Debugf("====bbbbbb====key========%s", key)
	return c.refresh(fqdn, obj)
}

func (c *Controller) refresh(rootDomain string, obj *extensionsv1beta1.Ingress) error {
	if obj.ObjectMeta.DeletionTimestamp != nil {
		return nil
	}
	if obj.Annotations == nil {
		obj.Annotations = make(map[string]string)
	}
	hostname := obj.Annotations[annotationHostname]
	targetHostname := ""
	switch obj.Annotations[annotationIngressClass] {
	case "": // nginx as default
		fallthrough
	case ingressClassNginx:
		targetHostname = c.getRdnsHostname(obj, rootDomain)
		logrus.Debugf("====targetHostname=======%s", targetHostname)
	default:
		return nil
	}
	if hostname == targetHostname {
		return nil
	}

	newObj := obj.DeepCopy()
	newObj.Annotations[annotationHostname] = targetHostname

	// Also need to update rules for hostname when using nginx
	for i, rule := range newObj.Spec.Rules {
		logrus.Debugf("Got ingress resource hostname: %s", rule.Host)
		//if strings.HasSuffix(rule.Host, setting.GetRootDomain()) {
		newObj.Spec.Rules[i].Host = targetHostname
		//}
	}

	logrus.Debug("1111111111122222223333333")
	if _, err := c.ingressInterface.Update(newObj); err != nil {
		logrus.Debug("2222223333333444444")
		return err
	}

	return nil
}

func (c *Controller) refreshAll(rootDomain string) error {
	ingresses, err := c.ingressLister.List("", labels.NewSelector())
	if err != nil {
		return err
	}
	for _, obj := range ingresses {
		if err = c.refresh(rootDomain, obj); err != nil {
			logrus.WithError(err).Errorf("refresh ingress %s:%s hostname annotation error", obj.Namespace, obj.Name)
		}
	}
	return nil
}

func (c *Controller) getRdnsHostname(obj *extensionsv1beta1.Ingress, rootDomain string) string {
	return fmt.Sprintf("%s.%s.%s", obj.Name, obj.Namespace, rootDomain)
}

//getSecret return token and fqdn
func (c *Controller) getSecret() (string, string, error) {
	sec, err := c.managementSecretLister.Get(c.clusterName, secretKey)
	if err != nil {
		return "", "", err
	}
	return string(sec.Data["token"]), string(sec.Data["fqdn"]), nil
}
