// Package remote implements tester for ConfigMap.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"strings"
	"time"

	"github.com/aws/aws-k8s-tester/eksconfig"
	aws_ecr "github.com/aws/aws-k8s-tester/pkg/aws/ecr"
	k8s_client "github.com/aws/aws-k8s-tester/pkg/k8s-client"
	"github.com/aws/aws-k8s-tester/pkg/metrics"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ecr/ecriface"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	api_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/exec"
)

// Config defines configmaps configuration.
// ref. https://github.com/kubernetes/perf-tests
type Config struct {
	Logger    *zap.Logger
	Stopc     chan struct{}
	EKSConfig *eksconfig.Config
	K8SClient k8s_client.EKS
	ECRAPI    ecriface.ECRAPI
}

// Tester defines configmaps tester.
// ref. https://github.com/kubernetes/perf-tests
type Tester interface {
	// Create installs configmaps tester.
	Create() error
	// Delete deletes configmaps tester.
	Delete() error
	// AggregateResults aggregates all test results from remote nodes.
	AggregateResults() error
}

func New(cfg Config) (Tester, error) {
	return &tester{cfg: cfg}, nil
}

type tester struct {
	cfg      Config
	ecrImage string
}

func (ts *tester) Create() (err error) {
	if ts.cfg.EKSConfig.AddOnConfigMapsRemote.Created {
		ts.cfg.Logger.Info("skipping create AddOnConfigMapsRemote")
		return nil
	}

	ts.cfg.Logger.Info("starting configmap testing")
	ts.cfg.EKSConfig.AddOnConfigMapsRemote.Created = true
	ts.cfg.EKSConfig.Sync()
	createStart := time.Now()

	defer func() {
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.CreateTook = time.Since(createStart)
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.CreateTookString = ts.cfg.EKSConfig.AddOnConfigMapsRemote.CreateTook.String()
		ts.cfg.EKSConfig.Sync()
	}()

	if ts.ecrImage, err = aws_ecr.Check(
		ts.cfg.Logger,
		ts.cfg.ECRAPI,
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.RepositoryAccountID,
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.RepositoryName,
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.RepositoryImageTag,
	); err != nil {
		return err
	}
	if err = k8s_client.CreateNamespace(
		ts.cfg.Logger,
		ts.cfg.K8SClient.KubernetesClientSet(),
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
	); err != nil {
		return err
	}
	if err = ts.createServiceAccount(); err != nil {
		return err
	}
	if err = ts.createALBRBACClusterRole(); err != nil {
		return err
	}
	if err = ts.createALBRBACClusterRoleBinding(); err != nil {
		return err
	}
	if err = ts.createConfigMap(); err != nil {
		return err
	}
	if err = ts.createDeployment(); err != nil {
		return err
	}
	if err = ts.waitDeployment(); err != nil {
		return err
	}

	select {
	case <-ts.cfg.Stopc:
		ts.cfg.Logger.Warn("configmaps tester aborted")
		return errors.New("configmaps tester aborted")
	case <-time.After(15 * time.Second):
		// wait for results writes
	}

	waitDur, retryStart := 5*time.Minute, time.Now()
	for time.Now().Sub(retryStart) < waitDur {
		select {
		case <-ts.cfg.Stopc:
			ts.cfg.Logger.Warn("health check aborted")
			return nil
		case <-time.After(5 * time.Second):
		}
		err = ts.cfg.K8SClient.CheckHealth()
		if err == nil {
			break
		}
		ts.cfg.Logger.Warn("health check failed", zap.Error(err))
	}
	ts.cfg.EKSConfig.Sync()
	if err == nil {
		ts.cfg.Logger.Info("health check success after configmap testing")
	} else {
		ts.cfg.Logger.Warn("health check failed after configmap testing", zap.Error(err))
	}
	return err
}

func (ts *tester) Delete() error {
	if !ts.cfg.EKSConfig.AddOnConfigMapsRemote.Created {
		ts.cfg.Logger.Info("skipping delete AddOnConfigMapsRemote")
		return nil
	}

	deleteStart := time.Now()
	defer func() {
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.DeleteTook = time.Since(deleteStart)
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.DeleteTookString = ts.cfg.EKSConfig.AddOnConfigMapsRemote.DeleteTook.String()
		ts.cfg.EKSConfig.Sync()
	}()

	var errs []string

	if err := ts.deleteDeployment(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := ts.deleteConfigMap(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := ts.deleteALBRBACClusterRoleBinding(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := ts.deleteALBRBACClusterRole(); err != nil {
		errs = append(errs, err.Error())
	}
	if err := ts.deleteServiceAccount(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := k8s_client.DeleteNamespaceAndWait(
		ts.cfg.Logger,
		ts.cfg.K8SClient.KubernetesClientSet(),
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
		k8s_client.DefaultNamespaceDeletionInterval,
		k8s_client.DefaultNamespaceDeletionTimeout,
	); err != nil {
		errs = append(errs, fmt.Sprintf("failed to delete configmaps namespace (%v)", err))
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, ", "))
	}

	ts.cfg.EKSConfig.AddOnConfigMapsRemote.Created = false
	return ts.cfg.EKSConfig.Sync()
}

const (
	configmapsServiceAccountName          = "configmaps-remote-service-account"
	configmapsRBACRoleName                = "configmaps-remote-rbac-role"
	configmapsRBACClusterRoleBindingName  = "configmaps-remote-rbac-role-binding"
	configmapsKubeConfigConfigMapName     = "configmaps-remote-kubeconfig-config-map"
	configmapsKubeConfigConfigMapFileName = "configmaps-remote-kubeconfig-config-map.yaml"
	configmapsDeploymentName              = "configmaps-remote-deployment"
	configmapsAppName                     = "configmaps-remote-app"
)

// ref. https://github.com/kubernetes/client-go/tree/master/examples/in-cluster-client-configuration
// ref. https://kubernetes.io/docs/reference/access-authn-authz/rbac/
func (ts *tester) createServiceAccount() error {
	ts.cfg.Logger.Info("creating configmaps ServiceAccount")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	_, err := ts.cfg.K8SClient.KubernetesClientSet().
		CoreV1().
		ServiceAccounts(ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace).
		Create(
			ctx,
			&v1.ServiceAccount{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ServiceAccount",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      configmapsServiceAccountName,
					Namespace: ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": configmapsAppName,
					},
				},
			},
			metav1.CreateOptions{},
		)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to create configmaps ServiceAccount (%v)", err)
	}

	ts.cfg.Logger.Info("created configmaps ServiceAccount")
	return ts.cfg.EKSConfig.Sync()
}

// ref. https://github.com/kubernetes/client-go/tree/master/examples/in-cluster-client-configuration
// ref. https://kubernetes.io/docs/reference/access-authn-authz/rbac/
func (ts *tester) deleteServiceAccount() error {
	ts.cfg.Logger.Info("deleting configmaps ServiceAccount")
	foreground := metav1.DeletePropagationForeground
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := ts.cfg.K8SClient.KubernetesClientSet().
		CoreV1().
		ServiceAccounts(ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace).
		Delete(
			ctx,
			configmapsServiceAccountName,
			metav1.DeleteOptions{
				GracePeriodSeconds: aws.Int64(0),
				PropagationPolicy:  &foreground,
			},
		)
	cancel()
	if err != nil && !api_errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete configmaps ServiceAccount (%v)", err)
	}
	ts.cfg.Logger.Info("deleted configmaps ServiceAccount", zap.Error(err))

	return ts.cfg.EKSConfig.Sync()
}

// ref. https://github.com/kubernetes/client-go/tree/master/examples/in-cluster-client-configuration
// ref. https://kubernetes.io/docs/reference/access-authn-authz/rbac/
func (ts *tester) createALBRBACClusterRole() error {
	ts.cfg.Logger.Info("creating configmaps RBAC ClusterRole")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	_, err := ts.cfg.K8SClient.KubernetesClientSet().
		RbacV1().
		ClusterRoles().
		Create(
			ctx,
			&rbacv1.ClusterRole{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "rbac.authorization.k8s.io/v1",
					Kind:       "ClusterRole",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      configmapsRBACRoleName,
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/name": configmapsAppName,
					},
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{
							"*",
						},
						Resources: []string{
							"configmaps",
						},
						Verbs: []string{
							"create",
							"get",
							"list",
							"update",
							"watch",
							"patch",
						},
					},
				},
			},
			metav1.CreateOptions{},
		)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to create configmaps RBAC ClusterRole (%v)", err)
	}

	ts.cfg.Logger.Info("created configmaps RBAC ClusterRole")
	return ts.cfg.EKSConfig.Sync()
}

// ref. https://github.com/kubernetes/client-go/tree/master/examples/in-cluster-client-configuration
// ref. https://kubernetes.io/docs/reference/access-authn-authz/rbac/
func (ts *tester) deleteALBRBACClusterRole() error {
	ts.cfg.Logger.Info("deleting configmaps RBAC ClusterRole")
	foreground := metav1.DeletePropagationForeground
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := ts.cfg.K8SClient.KubernetesClientSet().
		RbacV1().
		ClusterRoles().
		Delete(
			ctx,
			configmapsRBACRoleName,
			metav1.DeleteOptions{
				GracePeriodSeconds: aws.Int64(0),
				PropagationPolicy:  &foreground,
			},
		)
	cancel()
	if err != nil && !api_errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete configmaps RBAC ClusterRole (%v)", err)
	}

	ts.cfg.Logger.Info("deleted configmaps RBAC ClusterRole", zap.Error(err))
	return ts.cfg.EKSConfig.Sync()
}

// ref. https://github.com/kubernetes/client-go/tree/master/examples/in-cluster-client-configuration
// ref. https://kubernetes.io/docs/reference/access-authn-authz/rbac/
func (ts *tester) createALBRBACClusterRoleBinding() error {
	ts.cfg.Logger.Info("creating configmaps RBAC ClusterRoleBinding")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	_, err := ts.cfg.K8SClient.KubernetesClientSet().
		RbacV1().
		ClusterRoleBindings().
		Create(
			ctx,
			&rbacv1.ClusterRoleBinding{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "rbac.authorization.k8s.io/v1",
					Kind:       "ClusterRoleBinding",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      configmapsRBACClusterRoleBindingName,
					Namespace: "default",
					Labels: map[string]string{
						"app.kubernetes.io/name": configmapsAppName,
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     configmapsRBACRoleName,
				},
				Subjects: []rbacv1.Subject{
					{
						APIGroup:  "",
						Kind:      "ServiceAccount",
						Name:      configmapsServiceAccountName,
						Namespace: ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
					},
					{ // https://kubernetes.io/docs/reference/access-authn-authz/rbac/
						APIGroup: "rbac.authorization.k8s.io",
						Kind:     "User",
						Name:     "system:node",
					},
				},
			},
			metav1.CreateOptions{},
		)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to create configmaps RBAC ClusterRoleBinding (%v)", err)
	}

	ts.cfg.Logger.Info("created configmaps RBAC ClusterRoleBinding")
	return ts.cfg.EKSConfig.Sync()
}

// ref. https://github.com/kubernetes/client-go/tree/master/examples/in-cluster-client-configuration
// ref. https://kubernetes.io/docs/reference/access-authn-authz/rbac/
func (ts *tester) deleteALBRBACClusterRoleBinding() error {
	ts.cfg.Logger.Info("deleting configmaps RBAC ClusterRoleBinding")
	foreground := metav1.DeletePropagationForeground
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := ts.cfg.K8SClient.KubernetesClientSet().
		RbacV1().
		ClusterRoleBindings().
		Delete(
			ctx,
			configmapsRBACClusterRoleBindingName,
			metav1.DeleteOptions{
				GracePeriodSeconds: aws.Int64(0),
				PropagationPolicy:  &foreground,
			},
		)
	cancel()
	if err != nil && !api_errors.IsNotFound(err) {
		return fmt.Errorf("failed to delete configmaps RBAC ClusterRoleBinding (%v)", err)
	}

	ts.cfg.Logger.Info("deleted configmaps RBAC ClusterRoleBinding", zap.Error(err))
	return ts.cfg.EKSConfig.Sync()
}

func (ts *tester) createConfigMap() error {
	ts.cfg.Logger.Info("creating config map")

	b, err := ioutil.ReadFile(ts.cfg.EKSConfig.KubeConfigPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	_, err = ts.cfg.K8SClient.KubernetesClientSet().
		CoreV1().
		ConfigMaps(ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace).
		Create(
			ctx,
			&v1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      configmapsKubeConfigConfigMapName,
					Namespace: ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
					Labels: map[string]string{
						"name": configmapsKubeConfigConfigMapName,
					},
				},
				Data: map[string]string{
					configmapsKubeConfigConfigMapFileName: string(b),
				},
			},
			metav1.CreateOptions{},
		)
	cancel()
	if err != nil {
		return err
	}

	ts.cfg.Logger.Info("created config map")
	return ts.cfg.EKSConfig.Sync()
}

func (ts *tester) deleteConfigMap() error {
	ts.cfg.Logger.Info("deleting config map")
	foreground := metav1.DeletePropagationForeground
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := ts.cfg.K8SClient.KubernetesClientSet().
		CoreV1().
		ConfigMaps(ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace).
		Delete(
			ctx,
			configmapsKubeConfigConfigMapName,
			metav1.DeleteOptions{
				GracePeriodSeconds: aws.Int64(0),
				PropagationPolicy:  &foreground,
			},
		)
	cancel()
	if err != nil {
		return err
	}
	ts.cfg.Logger.Info("deleted config map")
	return ts.cfg.EKSConfig.Sync()
}

func (ts *tester) createDeployment() error {
	// "/opt/"+configmapsKubeConfigConfigMapFileName,
	// do not specify "kubeconfig", and use in-cluster config via "pkg/k8s-client"
	// otherwise, error "namespaces is forbidden: User "system:node:ip-192-168-84..."
	// ref. https://github.com/kubernetes/client-go/blob/master/examples/in-cluster-client-configuration/main.go
	testerCmd := fmt.Sprintf("/aws-k8s-tester eks create configmaps --clients=%d --client-qps=%f --client-burst=%d --client-timeout=%s --namespace=%s --objects=%d --object-size=%d --writes-output-name-prefix=%s --block=true",
		ts.cfg.EKSConfig.Clients,
		ts.cfg.EKSConfig.ClientQPS,
		ts.cfg.EKSConfig.ClientBurst,
		ts.cfg.EKSConfig.ClientTimeout,
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.Objects,
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.ObjectSize,
		ts.cfg.EKSConfig.AddOnConfigMapsRemote.RequestsSummaryWritesOutputNamePrefix,
	)

	ts.cfg.Logger.Info("creating configmaps Deployment", zap.String("image", ts.ecrImage), zap.String("tester-command", testerCmd))
	dirOrCreate := v1.HostPathDirectoryOrCreate
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	_, err := ts.cfg.K8SClient.KubernetesClientSet().
		AppsV1().
		Deployments(ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace).
		Create(
			ctx,
			&appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      configmapsDeploymentName,
					Namespace: ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name": configmapsAppName,
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: aws.Int32(ts.cfg.EKSConfig.AddOnConfigMapsRemote.DeploymentReplicas),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app.kubernetes.io/name": configmapsAppName,
						},
					},
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app.kubernetes.io/name": configmapsAppName,
							},
						},
						Spec: v1.PodSpec{
							ServiceAccountName: configmapsServiceAccountName,

							// TODO: set resource limits
							Containers: []v1.Container{
								{
									Name:            configmapsAppName,
									Image:           ts.ecrImage,
									ImagePullPolicy: v1.PullAlways,

									Command: []string{
										"/bin/sh",
										"-ec",
										testerCmd,
									},

									SecurityContext: &v1.SecurityContext{
										Privileged: aws.Bool(true),
									},

									// ref. https://kubernetes.io/docs/concepts/cluster-administration/logging/
									VolumeMounts: []v1.VolumeMount{
										{ // to execute
											Name:      configmapsKubeConfigConfigMapName,
											MountPath: "/opt",
										},
										{ // to write
											Name:      "varlog",
											MountPath: "/var/log",
											ReadOnly:  false,
										},
									},
								},
							},

							// ref. https://kubernetes.io/docs/concepts/cluster-administration/logging/
							Volumes: []v1.Volume{
								{ // to execute
									Name: configmapsKubeConfigConfigMapName,
									VolumeSource: v1.VolumeSource{
										ConfigMap: &v1.ConfigMapVolumeSource{
											LocalObjectReference: v1.LocalObjectReference{
												Name: configmapsKubeConfigConfigMapName,
											},
											DefaultMode: aws.Int32(0777),
										},
									},
								},
								{ // to write
									Name: "varlog",
									VolumeSource: v1.VolumeSource{
										HostPath: &v1.HostPathVolumeSource{
											Path: "/var/log",
											Type: &dirOrCreate,
										},
									},
								},
							},

							NodeSelector: map[string]string{
								// do not deploy in fake nodes, obviously
								"NodeType": "regular",
							},
						},
					},
				},
			},
			metav1.CreateOptions{},
		)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to create configmaps Deployment (%v)", err)
	}
	return nil
}

func (ts *tester) deleteDeployment() error {
	ts.cfg.Logger.Info("deleting deployment")
	foreground := metav1.DeletePropagationForeground
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	err := ts.cfg.K8SClient.KubernetesClientSet().
		AppsV1().
		Deployments(ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace).
		Delete(
			ctx,
			configmapsDeploymentName,
			metav1.DeleteOptions{
				GracePeriodSeconds: aws.Int64(0),
				PropagationPolicy:  &foreground,
			},
		)
	cancel()
	if err != nil {
		return err
	}
	ts.cfg.Logger.Info("deleted deployment")
	return ts.cfg.EKSConfig.Sync()
}

func (ts *tester) waitDeployment() error {
	ts.cfg.Logger.Info("waiting for configmaps Deployment")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	output, err := exec.New().CommandContext(
		ctx,
		ts.cfg.EKSConfig.KubectlPath,
		"--kubeconfig="+ts.cfg.EKSConfig.KubeConfigPath,
		"--namespace="+ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace,
		"describe",
		"deployment",
		configmapsDeploymentName,
	).CombinedOutput()
	cancel()
	if err != nil {
		return fmt.Errorf("'kubectl describe deployment' failed %v", err)
	}
	out := string(output)
	fmt.Printf("\n\n\"kubectl describe deployment\" output:\n%s\n\n", out)

	ready := false
	waitDur := 5*time.Minute + time.Duration(ts.cfg.EKSConfig.AddOnConfigMapsRemote.DeploymentReplicas)*time.Minute
	retryStart := time.Now()
	for time.Now().Sub(retryStart) < waitDur {
		select {
		case <-ts.cfg.Stopc:
			return errors.New("check aborted")
		case <-time.After(15 * time.Second):
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		dresp, err := ts.cfg.K8SClient.KubernetesClientSet().
			AppsV1().
			Deployments(ts.cfg.EKSConfig.AddOnConfigMapsRemote.Namespace).
			Get(ctx, configmapsDeploymentName, metav1.GetOptions{})
		cancel()
		if err != nil {
			return fmt.Errorf("failed to get Deployment (%v)", err)
		}
		ts.cfg.Logger.Info("get deployment",
			zap.Int32("desired-replicas", dresp.Status.Replicas),
			zap.Int32("available-replicas", dresp.Status.AvailableReplicas),
			zap.Int32("unavailable-replicas", dresp.Status.UnavailableReplicas),
			zap.Int32("ready-replicas", dresp.Status.ReadyReplicas),
		)
		available := false
		for _, cond := range dresp.Status.Conditions {
			ts.cfg.Logger.Info("condition",
				zap.String("last-updated", cond.LastUpdateTime.String()),
				zap.String("type", string(cond.Type)),
				zap.String("status", string(cond.Status)),
				zap.String("reason", cond.Reason),
				zap.String("message", cond.Message),
			)
			if cond.Status != v1.ConditionTrue {
				continue
			}
			if cond.Type == appsv1.DeploymentAvailable {
				available = true
				break
			}
		}
		if available && dresp.Status.AvailableReplicas >= ts.cfg.EKSConfig.AddOnConfigMapsRemote.DeploymentReplicas {
			ready = true
			break
		}
	}
	if !ready {
		// TODO: return error...
		// return errors.New("Deployment not ready")
		ts.cfg.Logger.Warn("Deployment not ready")
	}

	ts.cfg.Logger.Info("waited for configmaps Deployment")
	return ts.cfg.EKSConfig.Sync()
}

func (ts *tester) AggregateResults() (err error) {
	if !ts.cfg.EKSConfig.AddOnConfigMapsRemote.Created {
		ts.cfg.Logger.Info("skipping aggregating AddOnConfigMapsRemote")
		return nil
	}

	ts.cfg.Logger.Info("aggregating results from Pods")
	writes := metrics.RequestsSummary{}
	if ts.cfg.EKSConfig.IsEnabledAddOnNodeGroups() && ts.cfg.EKSConfig.AddOnNodeGroups.FetchLogs {
		ts.cfg.Logger.Info("fetching logs from ngs")
		for _, v := range ts.cfg.EKSConfig.AddOnNodeGroups.ASGs {
			for _, fpaths := range v.Logs {
				for _, fpath := range fpaths {
					if strings.Contains(fpath, ts.cfg.EKSConfig.AddOnConfigMapsRemote.RequestsSummaryWritesOutputNamePrefix) && strings.HasSuffix(fpath, "-writes.json") {
						b, err := ioutil.ReadFile(fpath)
						if err != nil {
							return fmt.Errorf("failed to open %q (%v)", fpath, err)
						}
						var r metrics.RequestsSummary
						if err = json.Unmarshal(b, &r); err != nil {
							return fmt.Errorf("failed to unmarshal %q (%s, %v)", fpath, string(b), err)
						}
						writes.SuccessTotal += r.SuccessTotal
						writes.FailureTotal += r.FailureTotal
						if writes.LatencyHistogram == nil || len(writes.LatencyHistogram) == 0 {
							writes.LatencyHistogram = r.LatencyHistogram
						} else {
							writes.LatencyHistogram, err = metrics.MergeHistograms(writes.LatencyHistogram, r.LatencyHistogram)
							if err != nil {
								return fmt.Errorf("failed to merge histograms (%v)", err)
							}
						}
					}
				}
			}
		}
	}
	if ts.cfg.EKSConfig.IsEnabledAddOnManagedNodeGroups() && ts.cfg.EKSConfig.AddOnManagedNodeGroups.FetchLogs {
		ts.cfg.Logger.Info("fetching logs from mngs")
		for _, cur := range ts.cfg.EKSConfig.AddOnManagedNodeGroups.MNGs {
			for _, fpaths := range cur.Logs {
				for _, fpath := range fpaths {
					if strings.Contains(fpath, ts.cfg.EKSConfig.AddOnConfigMapsRemote.RequestsSummaryWritesOutputNamePrefix) && strings.HasSuffix(fpath, "-writes.json") {
						b, err := ioutil.ReadFile(fpath)
						if err != nil {
							return fmt.Errorf("failed to open %q (%v)", fpath, err)
						}
						var r metrics.RequestsSummary
						if err = json.Unmarshal(b, &r); err != nil {
							return fmt.Errorf("failed to unmarshal %q (%s, %v)", fpath, string(b), err)
						}
						writes.SuccessTotal += r.SuccessTotal
						writes.FailureTotal += r.FailureTotal
						if writes.LatencyHistogram == nil || len(writes.LatencyHistogram) == 0 {
							writes.LatencyHistogram = r.LatencyHistogram
						} else {
							writes.LatencyHistogram, err = metrics.MergeHistograms(writes.LatencyHistogram, r.LatencyHistogram)
							if err != nil {
								return fmt.Errorf("failed to merge histograms (%v)", err)
							}
						}
					}
				}
			}
		}
	}

	ts.cfg.EKSConfig.AddOnConfigMapsRemote.RequestsSummaryWrites = writes
	ts.cfg.EKSConfig.Sync()

	if err = ioutil.WriteFile(ts.cfg.EKSConfig.AddOnConfigMapsRemote.RequestsSummaryWritesJSONPath, []byte(writes.JSON()), 0600); err != nil {
		ts.cfg.Logger.Warn("failed to write file", zap.Error(err))
		return err
	}
	if err = ioutil.WriteFile(ts.cfg.EKSConfig.AddOnConfigMapsRemote.RequestsSummaryWritesTablePath, []byte(writes.Table()), 0600); err != nil {
		ts.cfg.Logger.Warn("failed to write file", zap.Error(err))
		return err
	}
	fmt.Printf("\n\nAddOnConfigMapsRemote.RequestsSummaryWritesTable:\n%s\n", writes.Table())

	ts.cfg.Logger.Info("aggregated results from Pods")
	return ts.cfg.EKSConfig.Sync()
}