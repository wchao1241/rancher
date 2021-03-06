package manager

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"crypto/sha256"

	"github.com/pkg/errors"
	"github.com/rancher/rancher/pkg/catalog/utils"
	"github.com/rancher/types/apis/management.cattle.io/v3"
	"github.com/sirupsen/logrus"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	CatalogNameLabel  = "catalog.cattle.io/name"
	TemplateNameLabel = "catalog.cattle.io/template_name"
)

// update will sync templates with catalog without costing too much
func (m *Manager) update(catalog *v3.Catalog, templates []v3.Template, toDeleteTemplate []string, versionCommits map[string]v3.VersionCommits, commit string) error {
	logrus.Debugf("Syncing catalog %s with templates", catalog.Name)

	// delete non-existing templates
	for _, toDelete := range toDeleteTemplate {
		logrus.Infof("Deleting template %s and its associated templateVersion", toDelete)
		toDeleteTvs, err := m.getTemplateVersion(toDelete)
		if err != nil {
			return err
		}
		for tv := range toDeleteTvs {
			if err := m.templateVersionClient.Delete(tv, &metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
				return err
			}
		}
		if err := m.templateClient.Delete(toDelete, &metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
			return err
		}
	}

	// list all existing templates
	templateMap, err := m.getTemplateMap(catalog.Name)
	if err != nil {
		return err
	}

	// list all templateContent tag
	templateContentMap := map[string]struct{}{}
	templateContentList, err := m.templateContentClient.List(metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, t := range templateContentList.Items {
		templateContentMap[t.Name] = struct{}{}
	}

	errs := []error{}
	for _, template := range templates {
		var temErr error
		// look template by name, if not found then create it, otherwise do update
		if existing, ok := templateMap[template.Name]; ok {
			if err := m.updateTemplate(&existing, template, templateContentMap); err != nil {
				temErr = err
			}
		} else {
			if err := m.createTemplate(template, catalog, templateContentMap); err != nil {
				temErr = err
			}
		}
		if temErr != nil {
			delete(versionCommits, template.Spec.DisplayName)
			errs = append(errs, temErr)
		}
	}
	catalog.Status.HelmVersionCommits = versionCommits

	if len(errs) > 0 {
		if _, err := m.catalogClient.Update(catalog); err != nil {
			return err
		}
		return errors.Errorf("failed to update templates. Multiple error occurred: %v", errs)
	}

	v3.CatalogConditionRefreshed.True(catalog)
	catalog.Status.Commit = commit
	if _, err := m.catalogClient.Update(catalog); err != nil {
		return err
	}
	return nil
}

func (m *Manager) createTemplate(template v3.Template, catalog *v3.Catalog, tagMap map[string]struct{}) error {
	template.Labels = map[string]string{
		CatalogNameLabel: catalog.Name,
	}
	versionFiles := make([]v3.TemplateVersionSpec, len(template.Spec.Versions))
	copy(versionFiles, template.Spec.Versions)
	for i := range template.Spec.Versions {
		template.Spec.Versions[i].Files = nil
		template.Spec.Versions[i].Readme = ""
		template.Spec.Versions[i].AppReadme = ""
	}
	if err := m.convertTemplateIcon(&template, tagMap); err != nil {
		return err
	}
	logrus.Infof("Creating template %s", template.Name)
	createdTemplate, err := m.templateClient.Create(&template)
	if err != nil {
		return errors.Wrapf(err, "failed to create template %s", template.Name)
	}
	return m.createTemplateVersions(versionFiles, createdTemplate, tagMap)
}

func (m *Manager) getTemplateMap(catalogName string) (map[string]v3.Template, error) {
	templateList, err := m.templateClient.List(metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", CatalogNameLabel, catalogName),
	})
	if err != nil {
		return nil, err
	}
	templateMap := map[string]v3.Template{}
	for _, t := range templateList.Items {
		templateMap[t.Name] = t
	}
	return templateMap, nil
}

func (m *Manager) convertTemplateIcon(template *v3.Template, tagMap map[string]struct{}) error {
	tag, content, err := zipAndHash(template.Spec.Icon)
	if err != nil {
		return err
	}
	if _, ok := tagMap[tag]; !ok {
		templateContent := &v3.TemplateContent{}
		templateContent.Name = tag
		templateContent.Data = content
		if _, err := m.templateContentClient.Create(templateContent); err != nil {
			return err
		}
		tagMap[tag] = struct{}{}
	}
	template.Spec.Icon = tag
	return nil
}

func (m *Manager) updateTemplate(template *v3.Template, toUpdate v3.Template, tagMap map[string]struct{}) error {
	templateVersions, err := m.templateVersionClient.List(metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", TemplateNameLabel, template.Name),
	})
	if err != nil {
		return errors.Wrapf(err, "failed to list templateVersions")
	}
	tvByVersion := map[string]v3.TemplateVersion{}
	for _, ver := range templateVersions.Items {
		tvByVersion[ver.Spec.Version] = ver
	}
	/*
		for each templateVersion in toUpdate, calculate each hash value and if doesn't match, do update.
		For version that doesn't exist, create a new one
	*/
	for _, toUpdateVer := range toUpdate.Spec.Versions {
		// gzip each file to store the hash value into etcd. Next time if it already exists in etcd then use the existing tag
		templateVersion := &v3.TemplateVersion{}
		templateVersion.Spec = toUpdateVer
		if err := m.convertFile(templateVersion, toUpdateVer, tagMap); err != nil {
			return err
		}
		if tv, ok := tvByVersion[toUpdateVer.Version]; ok {
			if tv.Spec.Digest != toUpdateVer.Digest {
				tv.Spec = templateVersion.Spec
				logrus.Infof("Updating templateVersion %v", tv.Name)
				if _, err := m.templateVersionClient.Update(&tv); err != nil {
					return err
				}
			}
		} else {
			toCreate := &v3.TemplateVersion{}
			toCreate.Name = fmt.Sprintf("%s-%v", template.Name, toUpdateVer.Version)
			toCreate.Labels = map[string]string{
				TemplateNameLabel: template.Name,
			}
			toCreate.Spec = templateVersion.Spec
			logrus.Infof("Creating templateVersion %v", toCreate.Name)
			if _, err := m.templateVersionClient.Create(toCreate); err != nil {
				return err
			}
		}
	}

	// find existing templateVersion that is not in toUpdate.Versions
	toUpdateTvs := map[string]struct{}{}
	for _, toUpdateVer := range toUpdate.Spec.Versions {
		toUpdateTvs[toUpdateVer.Version] = struct{}{}
	}
	for v, tv := range tvByVersion {
		if _, ok := toUpdateTvs[v]; !ok {
			logrus.Infof("Deleting templateVersion %s", tv.Name)
			if err := m.templateVersionClient.Delete(tv.Name, &metav1.DeleteOptions{}); err != nil && !kerrors.IsNotFound(err) {
				return err
			}
		}
	}

	for i := range toUpdate.Spec.Versions {
		toUpdate.Spec.Versions[i].Files = nil
		toUpdate.Spec.Versions[i].Readme = ""
		toUpdate.Spec.Versions[i].AppReadme = ""
	}
	template.Spec = toUpdate.Spec
	if err := m.convertTemplateIcon(template, tagMap); err != nil {
		return err
	}
	if _, err := m.templateClient.Update(template); err != nil {
		return err
	}
	return nil
}

func (m *Manager) getTemplateVersion(templateName string) (map[string]struct{}, error) {
	templateVersions, err := m.templateVersionClient.List(metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", TemplateNameLabel, templateName),
	})
	if err != nil {
		return nil, err
	}
	tVersion := map[string]struct{}{}
	for _, ver := range templateVersions.Items {
		tVersion[ver.Name] = struct{}{}
	}
	return tVersion, nil
}

func (m *Manager) createTemplateVersions(versionsSpec []v3.TemplateVersionSpec, template *v3.Template, tagMap map[string]struct{}) error {
	for _, spec := range versionsSpec {
		templateVersion := &v3.TemplateVersion{}
		templateVersion.Spec = spec
		templateVersion.Name = fmt.Sprintf("%s-%v", template.Name, spec.Version)
		templateVersion.Labels = map[string]string{
			TemplateNameLabel: template.Name,
		}
		// gzip each file to store the hash value into etcd. Next time if it already exists in etcd then use the existing tag
		if err := m.convertFile(templateVersion, spec, tagMap); err != nil {
			return err
		}

		logrus.Debugf("Creating templateVersion %s", templateVersion.Name)
		if _, err := m.templateVersionClient.Create(templateVersion); err != nil && !kerrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

func (m *Manager) convertFile(templateVersion *v3.TemplateVersion, spec v3.TemplateVersionSpec, tagMap map[string]struct{}) error {
	for name, file := range spec.Files {
		tag, content, err := zipAndHash(file)
		if err != nil {
			return err
		}
		if _, ok := tagMap[tag]; !ok {
			templateContent := &v3.TemplateContent{}
			templateContent.Name = tag
			templateContent.Data = content
			if _, err := m.templateContentClient.Create(templateContent); err != nil {
				return err
			}
			tagMap[tag] = struct{}{}
		}
		templateVersion.Spec.Files[name] = tag
		if file == spec.Readme {
			templateVersion.Spec.Readme = tag
		}
		if file == spec.AppReadme {
			templateVersion.Spec.AppReadme = tag
		}
	}
	return nil
}

func zipAndHash(content string) (string, string, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(content)); err != nil {
		return "", "", err
	}
	zw.Close()
	digest := sha256.New()
	compressedData := buf.Bytes()
	digest.Write(compressedData)
	tag := hex.EncodeToString(digest.Sum(nil))
	return tag, base64.StdEncoding.EncodeToString(compressedData), nil
}

func showUpgradeLinks(version, upgradeVersion string) bool {
	if !utils.VersionGreaterThan(upgradeVersion, version) {
		return false
	}
	return true
}
