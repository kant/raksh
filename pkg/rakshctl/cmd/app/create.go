// Copyright 2019 IBM Corp
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ghodss/yaml"
	securecontainersv1alpha1 "github.com/ibm/raksh/pkg/apis/securecontainers/v1alpha1"
	"github.com/ibm/raksh/pkg/crypto"
	typeflags "github.com/ibm/raksh/pkg/rakshctl/types/flags"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/batch/v2alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

var (
	createLong = `
		Modify K8s YAML and add changes required for secure container`

	createExample = `
		# Examples goes here`
	filename             string
	output               string
	secureContainerImage string
	scratchImage         string

	UnsupportedKindMsg = "Skipping the %s: unsupported object type"
)

const (
	securePrefix = "secure-"
)

func NewCmdAppCreate() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "create",
		DisableFlagsInUseLine: true,
		Short:                 "Create SecureContainer App",
		Long:                  createLong,
		Example:               createExample,
		RunE: func(cmd *cobra.Command, args []string) error {
			return main()
		},
	}

	cmd.Flags().AddFlagSet(Appflags)

	return cmd
}

func main() error {
	if filename == "" || secureContainerImage == "" {
		return errors.New("Required flag(s) filename and image")
	}
	files, err := walkAllManifests(filename)
	if err != nil {
		return err
	}
	//TODO: Convert this code to go routine
	for _, file := range files {
		fmt.Printf("Processing %s...\n", file)
		fin, err := os.Open(file)
		defer fin.Close()
		if err != nil {
			return err
		}
		reader := yamlutil.NewYAMLReader(bufio.NewReaderSize(fin, 4096))
		raw, err := reader.Read()
		obj, err := RawToObject(raw)
		if err != nil && !runtime.IsNotRegisteredError(err) {
			return err
		} else if runtime.IsNotRegisteredError(err) {
			fmt.Printf(UnsupportedKindMsg+"\n", file)
			continue
		}
		scObj, cmObj, err := secureObject(obj)
		if err != nil {
			return err
		}
		var outf *os.File
		secureFile := genSecureFile(file)
		if output != "" {
			secureFile = path.Join(output, secureFile)
			os.MkdirAll(filepath.Dir(secureFile), os.ModePerm)
		}
		if outf, err = os.Create(secureFile); err != nil {
			return err
		}
		defer outf.Close()

		err = writeObjTo(cmObj, outf)
		if err != nil {
			return err
		}
		err = writeObjTo(scObj, outf)
		if err != nil {
			return err
		}
		fmt.Println("Wrote to ", secureFile)
		fmt.Printf("Processing %s...: DONE\n", file)
	}
	return nil
}

// genSecureFile postfix the filename '-sc'
func genSecureFile(file string) string {
	return strings.TrimSuffix(file, path.Ext(file)) + "-sc" + path.Ext(file)
}

// walkAllManifests returns all the yaml files in a given directory
func walkAllManifests(dir string) ([]string, error) {
	files := []string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Printf("prevent panic by handling failure accessing a path %q: %v\n", path, err)
			return err
		}
		if filepath.Ext(info.Name()) == ".yaml" || filepath.Ext(info.Name()) == ".yml" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		fmt.Printf("error walking the path %q: %v\n", dir, err)
		return files, err
	}
	return files, nil
}

func RawToObject(raw []byte) (runtime.Object, error) {
	var typeMeta metav1.TypeMeta
	if err := yaml.Unmarshal(raw, &typeMeta); err != nil {
		return nil, err
	}

	gvk := schema.FromAPIVersionAndKind(typeMeta.APIVersion, typeMeta.Kind)
	obj, err := scheme.New(gvk)
	if err != nil {
		return nil, err
	}
	if err = yaml.Unmarshal(raw, obj); err != nil {
		return nil, err
	}

	return obj, nil
}

type SecureContainerConfigmapPodSpec struct {
	Containers []Container `json:"containers"`
}

type Container struct {
	Name      string                       `json:"name"`
	Image     string                       `json:"image,omitempty"`
	Command   []string                     `json:"command,omitempty"`
	Args      []string                     `json:"args,omitempty"`
	Env       []corev1.EnvVar              `json:"env,omitempty"`
	Ports     []corev1.ContainerPort       `json:"ports,omitempty"`
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

func newConfigMap(name, namespace string) corev1.ConfigMap {
	return corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Data:       map[string]string{},
	}
}

func newSecureContainer(name, secureContainerImage string, obj runtime.Object) securecontainersv1alpha1.SecureContainer {
	return securecontainersv1alpha1.SecureContainer{
		TypeMeta: metav1.TypeMeta{
			Kind:       "SecureContainer",
			APIVersion: "securecontainers.k8s.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Object: runtime.RawExtension{
			Object: obj,
		},
		Spec: securecontainersv1alpha1.SecureContainerSpec{
			SecureContainerImageRef: securecontainersv1alpha1.SecureContainerImageRef{
				Name: secureContainerImage,
			},
		},
	}
}

func mountConfigMap(pod *corev1.PodSpec, cm corev1.ConfigMap) {
	volumes := []corev1.Volume{}

	for index := range pod.Containers {
		volumeName := securePrefix + "volume-" + pod.Containers[index].Name
		volume := corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cm.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key:  pod.Containers[index].Name,
							Path: "raksh.properties",
						},
					},
				},
			},
		}
		volumes = append(volumes, volume)
		volmount := corev1.VolumeMount{
			Name:      volumeName,
			ReadOnly:  true,
			MountPath: "/etc/raksh",
		}
		pod.Containers[index].VolumeMounts = append(pod.Containers[index].VolumeMounts, volmount)
	}
	pod.Volumes = append(pod.Volumes, volumes...)
}

func secureObject(in runtime.Object) (securecontainersv1alpha1.SecureContainer, corev1.ConfigMap, error) {
	out := in.DeepCopyObject()

	var deploymentMetadata *metav1.ObjectMeta
	var podSpec *corev1.PodSpec
	var cmObj corev1.ConfigMap
	var scObj securecontainersv1alpha1.SecureContainer

	switch v := out.(type) {
	case *v2alpha1.CronJob:
		job := v
		deploymentMetadata = &job.ObjectMeta
		podSpec = &job.Spec.JobTemplate.Spec.Template.Spec
	case *corev1.Pod:
		pod := v
		deploymentMetadata = &pod.ObjectMeta
		podSpec = &pod.Spec
	case *appsv1.Deployment:
		deploy := v
		deploymentMetadata = &deploy.ObjectMeta
		podSpec = &deploy.Spec.Template.Spec
	default:
		outValue := reflect.ValueOf(out).Elem()

		deploymentMetadata = outValue.FieldByName("ObjectMeta").Addr().Interface().(*metav1.ObjectMeta)

		templateValue := outValue.FieldByName("Spec").FieldByName("Template")
		// `Template` is defined as a pointer in some older API
		// definitions, e.g. ReplicationController
		if templateValue.Kind() == reflect.Ptr {
			if templateValue.IsNil() {
				return scObj, cmObj, fmt.Errorf("spec.template is required value")
			}
			templateValue = templateValue.Elem()
		}
		podSpec = templateValue.FieldByName("Spec").Addr().Interface().(*corev1.PodSpec)
	}
	cmObj = newConfigMap(securePrefix+"configmap-"+deploymentMetadata.Name, deploymentMetadata.Namespace)

	for _, container := range podSpec.Containers {
		var cSpec Container
		c, err := yaml.Marshal(container)
		if err != nil {
			return scObj, cmObj, err
		}
		err = yaml.Unmarshal(c, &cSpec)
		// TODO: Temproray code to work with kata agent, refactor later with agent code
		configData := struct {
			Spec SecureContainerConfigmapPodSpec `json:"spec"`
		}{
			Spec: SecureContainerConfigmapPodSpec{
				Containers: []Container{cSpec},
			},
		}
		if err != nil {
			return scObj, cmObj, err
		}
		cbytes, err := yaml.Marshal(configData)
		if err != nil {
			return scObj, cmObj, err
		}
		cmObj.Data[container.Name] = string(cbytes)

		// TODO - Move the symm key logic to create initrd command
		encConfigMap, err := crypto.EncryptConfigMap(cbytes, typeflags.Key)
		if err != nil {
			return scObj, cmObj, err
		}

		cmObj.Data[container.Name] = encConfigMap
	}

	maskSensitiveData(podSpec)
	mountConfigMap(podSpec, cmObj)

	if typeflags.VaultSecret != "" {
		insertVaultSecret(podSpec, typeflags.VaultSecret)
	}

	scObj = newSecureContainer(securePrefix+deploymentMetadata.Name, secureContainerImage, out.(runtime.Object))

	return scObj, cmObj, nil
}

func insertVaultSecret(pod *corev1.PodSpec, secretName string) {
	vaultSecrets := []corev1.EnvVar{
		{
			Name: "SC_VAULT_ADDR",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "vaultAdd",
				},
			},
		},
		{
			Name: "SC_VAULT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "vaultToken",
				},
			},
		},
		{
			Name: "SC_VAULT_SECRET",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "secretName",
				},
			},
		},
		{
			Name: "SC_VAULT_SYMM_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "keyName",
				},
			},
		},
	}
	for index := range pod.Containers {
		pod.Containers[index].Env = append(pod.Containers[index].Env, vaultSecrets...)
	}
}

func writeObjTo(obj interface{}, writer io.Writer) error {
	objBytes, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}

	if _, err = writer.Write([]byte("---\n")); err != nil {
		return err
	}

	if _, err = writer.Write(objBytes); err != nil {
		return err
	}
	return nil
}

func maskSensitiveData(pod *corev1.PodSpec) {
	for index := range pod.Containers {
		pod.Containers[index].Image = scratchImage
		pod.Containers[index].Command = []string{}
		pod.Containers[index].Args = []string{}
		pod.Containers[index].Env = []corev1.EnvVar{}
	}
}
