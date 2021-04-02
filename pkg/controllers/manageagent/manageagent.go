package manageagent

import (
	"context"
	"fmt"

	"github.com/rancher/fleet/pkg/agent"
	fleet "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	"github.com/rancher/fleet/pkg/config"
	fleetcontrollers "github.com/rancher/fleet/pkg/generated/controllers/fleet.cattle.io/v1alpha1"
	"github.com/rancher/wrangler/pkg/apply"
	corecontrollers "github.com/rancher/wrangler/pkg/generated/controllers/core/v1"
	"github.com/rancher/wrangler/pkg/relatedresource"
	"github.com/rancher/wrangler/pkg/yaml"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"github.com/sirupsen/logrus"
)

const (
	agentBundleName = "fleet-agent"
)

type handler struct {
	apply           apply.Apply
	systemNamespace string
	clusterCache    fleetcontrollers.ClusterCache
	bundleCache     fleetcontrollers.BundleCache
}

func Register(ctx context.Context,
	systemNamespace string,
	apply apply.Apply,
	namespace corecontrollers.NamespaceController,
	clusters fleetcontrollers.ClusterController,
	bundle fleetcontrollers.BundleController,
) {
	h := handler{
		systemNamespace: systemNamespace,
		clusterCache:    clusters.Cache(),
		bundleCache:     bundle.Cache(),
		apply: apply.
			WithSetID("fleet-manage-agent").
			WithCacheTypes(bundle),
	}

	namespace.OnChange(ctx, "manage-agent", h.OnNamespace)
	clusters.OnChange(ctx, "manage-agent-cluster", h.OnCluster)
	relatedresource.WatchClusterScoped(ctx, "manage-agent-resolver", h.resolveNS, namespace, clusters)
}

func (h *handler) resolveNS(namespace, name string, obj runtime.Object) ([]relatedresource.Key, error) {
	if _, ok := obj.(*fleet.Cluster); ok {
		if _, err := h.bundleCache.Get(namespace, agentBundleName); err != nil {
			return []relatedresource.Key{{Name: namespace}}, nil
		}
	}
	return nil, nil
}

func (h *handler) OnCluster(key string, cluster *fleet.Cluster) (*fleet.Cluster, error) {
	logrus.Infof("manageAgentCluster: entered onCluster")
	
	return cluster, nil
}

func (h *handler) OnNamespace(key string, namespace *corev1.Namespace) (*corev1.Namespace, error) {
	if namespace == nil {
		return nil, nil
	}

	clusters, err := h.clusterCache.List(namespace.Name, labels.Everything())
	if err != nil {
		return nil, err
	}

	if len(clusters) == 0 {
		return namespace, nil
	}

	if namespace.Name != "fleet-default" {
			logrus.Infof("manageAgent: returning from onNamespace: %s", namespace.Name)
			return namespace, nil
	}

	for _, cluster := range clusters {
		logrus.Infof("manageAgent: trying to get agent bundle %s %s", namespace.Name, cluster.Name)
		objs, err := h.getAgentBundle(namespace.Name, cluster)
		if err != nil {
			return nil, err
		}

		err = h.apply.ApplyObjects(objs...)
		if err != nil {
			logrus.Errorf("error applying objects %v", err)
			return nil, err
		}
	}

	return namespace, nil
}

func (h *handler) getAgentBundle(ns string, cluster *fleet.Cluster) ([]runtime.Object, error) {
	cfg := config.Get()
	if cfg.ManageAgent != nil && !*cfg.ManageAgent {
		return nil, nil
	}

	objs := agent.Manifest(h.systemNamespace, cfg.AgentImage, cfg.AgentImagePullPolicy, "bundle", cfg.AgentCheckinInternal.Duration.String())
	agentYAML, err := yaml.Export(objs...)
	if err != nil {
		return nil, err
	}

	return []runtime.Object{
		&fleet.Bundle{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s",agentBundleName, cluster.Name),
				Namespace: ns,
			},
			Spec: fleet.BundleSpec{
				BundleDeploymentOptions: fleet.BundleDeploymentOptions{
					DefaultNamespace: h.systemNamespace,
					Helm: &fleet.HelmOptions{
						TakeOwnership: true,
					},
				},
				Resources: []fleet.BundleResource{
					{
						Name:    "agent.yaml",
						Content: string(agentYAML),
					},
				},
				Targets: []fleet.BundleTarget{
					{
						ClusterSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "fleet.cattle.io/non-managed-agent",
									Operator: metav1.LabelSelectorOpDoesNotExist,
								},
							},
						},
					},

					{ClusterSelector: &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "management.cattle.io/cluster-name",
								Operator: metav1.LabelSelectorOpIn,
								Values: []string{cluster.Name},
							},
						},
					},

					},
				},
			},
		},
	}, nil
}
