//go:build e2e
// +build e2e

package hashicorp_vault_test

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/kubernetes"

	. "github.com/kedacore/keda/v2/tests/helper"
)

// Load environment variables from .env file
var _ = godotenv.Load("../../.env")

const (
	testName = "hashicorp-vault-test"
)

var (
	testNamespace              = fmt.Sprintf("%s-ns", testName)
	vaultNamespace             = "hashicorp-ns"
	deploymentName             = fmt.Sprintf("%s-deployment", testName)
	scaledObjectName           = fmt.Sprintf("%s-so", testName)
	triggerAuthenticationName  = fmt.Sprintf("%s-ta", testName)
	secretName                 = fmt.Sprintf("%s-secret", testName)
	postgreSQLStatefulSetName  = "postgresql"
	postgresqlPodName          = fmt.Sprintf("%s-0", postgreSQLStatefulSetName)
	postgreSQLUsername         = "test-user"
	postgreSQLPassword         = "test-password"
	postgreSQLDatabase         = "test_db"
	postgreSQLConnectionString = fmt.Sprintf("postgresql://%s:%s@postgresql.%s.svc.cluster.local:5432/%s?sslmode=disable",
		postgreSQLUsername, postgreSQLPassword, testNamespace, postgreSQLDatabase)
	minReplicaCount = 0
	maxReplicaCount = 2
)

type templateData struct {
	TestNamespace                    string
	DeploymentName                   string
	VaultNamespace                   string
	ScaledObjectName                 string
	TriggerAuthenticationName        string
	SecretName                       string
	HashiCorpToken                   string
	PostgreSQLStatefulSetName        string
	PostgreSQLConnectionStringBase64 string
	PostgreSQLUsername               string
	PostgreSQLPassword               string
	PostgreSQLDatabase               string
	MinReplicaCount                  int
	MaxReplicaCount                  int
}

type templateValues map[string]string

const (
	deploymentTemplate = `
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app: postgresql-update-worker
  name: {{.DeploymentName}}
  namespace: {{.TestNamespace}}
spec:
  replicas: 0
  selector:
    matchLabels:
      app: postgresql-update-worker
  template:
    metadata:
      labels:
        app: postgresql-update-worker
    spec:
      containers:
      - image: ghcr.io/kedacore/tests-postgresql
        imagePullPolicy: Always
        name: postgresql-processor-test
        command:
          - /app
          - update
        env:
          - name: TASK_INSTANCES_COUNT
            value: "6000"
          - name: CONNECTION_STRING
            valueFrom:
              secretKeyRef:
                name: {{.SecretName}}
                key: postgresql_conn_str
`

	secretTemplate = `
apiVersion: v1
kind: Secret
metadata:
  name: {{.SecretName}}
  namespace: {{.TestNamespace}}
type: Opaque
data:
  postgresql_conn_str: {{.PostgreSQLConnectionStringBase64}}
`

	triggerAuthenticationTemplate = `
apiVersion: keda.sh/v1alpha1
kind: TriggerAuthentication
metadata:
  name: {{.TriggerAuthenticationName}}
  namespace: {{.TestNamespace}}
spec:
  hashiCorpVault:
    address: http://vault.{{.VaultNamespace}}:8200
    authentication: token
    credential:
      token: {{.HashiCorpToken}}
    secrets:
    - parameter: connection
      key: connectionString
      path: secret/data/keda
`

	scaledObjectTemplate = `
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: {{.ScaledObjectName}}
  namespace: {{.TestNamespace}}
spec:
  scaleTargetRef:
    name: {{.DeploymentName}}
  pollingInterval: 5
  cooldownPeriod:  10
  minReplicaCount: {{.MinReplicaCount}}
  maxReplicaCount: {{.MaxReplicaCount}}
  triggers:
  - type: postgresql
    metadata:
      targetQueryValue: "4"
      activationTargetQueryValue: "5"
      query: "SELECT CEIL(COUNT(*) / 5) FROM task_instance WHERE state='running' OR state='queued'"
    authenticationRef:
      name: {{.TriggerAuthenticationName}}
`

	postgreSQLStatefulSetTemplate = `
apiVersion: apps/v1
kind: StatefulSet
metadata:
  labels:
    app: {{.PostgreSQLStatefulSetName}}
  name: {{.PostgreSQLStatefulSetName}}
  namespace: {{.TestNamespace}}
spec:
  replicas: 1
  serviceName: {{.PostgreSQLStatefulSetName}}
  selector:
    matchLabels:
      app: {{.PostgreSQLStatefulSetName}}
  template:
    metadata:
      labels:
        app: {{.PostgreSQLStatefulSetName}}
    spec:
      containers:
      - image: postgres:10.5
        name: postgresql
        env:
          - name: POSTGRES_USER
            value: {{.PostgreSQLUsername}}
          - name: POSTGRES_PASSWORD
            value: {{.PostgreSQLPassword}}
          - name: POSTGRES_DB
            value: {{.PostgreSQLDatabase}}
        ports:
          - name: postgresql
            protocol: TCP
            containerPort: 5432
`

	postgreSQLServiceTemplate = `
apiVersion: v1
kind: Service
metadata:
  labels:
    app: {{.PostgreSQLStatefulSetName}}
  name: {{.PostgreSQLStatefulSetName}}
  namespace: {{.TestNamespace}}
spec:
  ports:
  - port: 5432
    protocol: TCP
    targetPort: 5432
  selector:
    app: {{.PostgreSQLStatefulSetName}}
  type: ClusterIP
`

	lowLevelRecordsJobTemplate = `
apiVersion: batch/v1
kind: Job
metadata:
  labels:
    app: postgresql-insert-low-level-job
  name: postgresql-insert-low-level-job
  namespace: {{.TestNamespace}}
spec:
  template:
    metadata:
      labels:
        app: postgresql-insert-low-level-job
    spec:
      containers:
      - image: ghcr.io/kedacore/tests-postgresql
        imagePullPolicy: Always
        name: postgresql-processor-test
        command:
          - /app
          - insert
        env:
          - name: TASK_INSTANCES_COUNT
            value: "20"
          - name: CONNECTION_STRING
            valueFrom:
              secretKeyRef:
                name: {{.SecretName}}
                key: postgresql_conn_str
      restartPolicy: Never
  backoffLimit: 4
`

	insertRecordsJobTemplate = `
apiVersion: batch/v1
kind: Job
metadata:
  labels:
    app: postgresql-insert-job
  name: postgresql-insert-job
  namespace: {{.TestNamespace}}
spec:
  template:
    metadata:
      labels:
        app: postgresql-insert-job
    spec:
      containers:
      - image: ghcr.io/kedacore/tests-postgresql
        imagePullPolicy: Always
        name: postgresql-processor-test
        command:
          - /app
          - insert
        env:
          - name: TASK_INSTANCES_COUNT
            value: "10000"
          - name: CONNECTION_STRING
            valueFrom:
              secretKeyRef:
                name: {{.SecretName}}
                key: postgresql_conn_str
      restartPolicy: Never
  backoffLimit: 4
`
)

func TestPostreSQLScaler(t *testing.T) {
	// Create kubernetes resources for PostgreSQL server
	kc := GetKubernetesClient(t)
	data, postgreSQLtemplates := getPostgreSQLTemplateData()

	CreateKubernetesResources(t, kc, testNamespace, data, postgreSQLtemplates)
	hashiCorpToken := setupHashiCorpVault(t, kc)

	assert.True(t, WaitForStatefulsetReplicaReadyCount(t, kc, postgreSQLStatefulSetName, testNamespace, 1, 60, 3),
		"replica count should be %d after 3 minutes", 1)

	createTableSQL := "CREATE TABLE task_instance (id serial PRIMARY KEY,state VARCHAR(10));"
	ok, out, errOut, err := WaitForSuccessfulExecCommandOnSpecificPod(t, postgresqlPodName, testNamespace,
		fmt.Sprintf("psql -U %s -d %s -c \"%s\"", postgreSQLUsername, postgreSQLDatabase, createTableSQL), 60, 3)
	assert.True(t, ok, "executing a command on PostreSQL Pod should work; Output: %s, ErrorOutput: %s, Error: %s", out, errOut, err)

	// Create kubernetes resources for testing
	data, templates := getTemplateData()
	data.HashiCorpToken = RemoveANSI(hashiCorpToken)
	KubectlApplyMultipleWithTemplate(t, data, templates)
	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, testNamespace, minReplicaCount, 60, 3),
		"replica count should be %d after 3 minutes", minReplicaCount)

	testActivation(t, kc, data)
	testScaleUp(t, kc, data)
	testScaleDown(t, kc)

	// cleanup
	KubectlDeleteMultipleWithTemplate(t, data, templates)
	cleanupHashiCorpVault(t, kc)
	DeleteKubernetesResources(t, kc, testNamespace, data, postgreSQLtemplates)
}

func setupHashiCorpVault(t *testing.T, kc *kubernetes.Clientset) string {
	CreateNamespace(t, kc, vaultNamespace)

	_, err := ExecuteCommand("helm repo add hashicorp https://helm.releases.hashicorp.com")
	assert.NoErrorf(t, err, "cannot add hashicorp repo - %s", err)

	_, err = ExecuteCommand("helm repo update")
	assert.NoErrorf(t, err, "cannot update repos - %s", err)

	_, err = ExecuteCommand(fmt.Sprintf(`helm upgrade --install --set server.dev.enabled=true --namespace %s --wait vault hashicorp/vault`, vaultNamespace))
	assert.NoErrorf(t, err, "cannot install hashicorp vault - %s", err)

	podName := "vault-0"

	_, _, err = ExecCommandOnSpecificPod(t, podName, vaultNamespace, fmt.Sprintf("vault kv put secret/keda connectionString=%s", postgreSQLConnectionString))
	assert.NoErrorf(t, err, "cannot put connection string in hashicorp vault - %s", err)

	out, _, err := ExecCommandOnSpecificPod(t, podName, vaultNamespace, "vault token create -field token")
	assert.NoErrorf(t, err, "cannot create hashicorp vault token - %s", err)

	return out
}

func cleanupHashiCorpVault(t *testing.T, kc *kubernetes.Clientset) {
	_, err := ExecuteCommand(fmt.Sprintf("helm uninstall vault --namespace %s", vaultNamespace))
	assert.NoErrorf(t, err, "cannot uninstall hashicorp vault - %s", err)

	_, err = ExecuteCommand("helm repo remove hashicorp")
	assert.NoErrorf(t, err, "cannot remove hashicorp repo - %s", err)

	DeleteNamespace(t, kc, vaultNamespace)
}

func testActivation(t *testing.T, kc *kubernetes.Clientset, data templateData) {
	t.Log("--- testing activation ---")
	templateTriggerJob := templateValues{"lowLevelRecordsJobTemplate": lowLevelRecordsJobTemplate}
	KubectlApplyMultipleWithTemplate(t, data, templateTriggerJob)

	AssertReplicaCountNotChangeDuringTimePeriod(t, kc, deploymentName, testNamespace, minReplicaCount, 60)
}

func testScaleUp(t *testing.T, kc *kubernetes.Clientset, data templateData) {
	t.Log("--- testing scale up ---")
	templateTriggerJob := templateValues{"insertRecordsJobTemplate": insertRecordsJobTemplate}
	KubectlApplyMultipleWithTemplate(t, data, templateTriggerJob)

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, testNamespace, maxReplicaCount, 60, 3),
		"replica count should be %d after 3 minutes", maxReplicaCount)
}

func testScaleDown(t *testing.T, kc *kubernetes.Clientset) {
	t.Log("--- testing scale down ---")

	assert.True(t, WaitForDeploymentReplicaReadyCount(t, kc, deploymentName, testNamespace, minReplicaCount, 60, 3),
		"replica count should be %d after 3 minutes", minReplicaCount)
}

var data = templateData{
	TestNamespace:                    testNamespace,
	PostgreSQLStatefulSetName:        postgreSQLStatefulSetName,
	DeploymentName:                   deploymentName,
	ScaledObjectName:                 scaledObjectName,
	MinReplicaCount:                  minReplicaCount,
	MaxReplicaCount:                  maxReplicaCount,
	TriggerAuthenticationName:        triggerAuthenticationName,
	SecretName:                       secretName,
	PostgreSQLUsername:               postgreSQLUsername,
	PostgreSQLPassword:               postgreSQLPassword,
	PostgreSQLDatabase:               postgreSQLDatabase,
	PostgreSQLConnectionStringBase64: base64.StdEncoding.EncodeToString([]byte(postgreSQLConnectionString)),
	VaultNamespace:                   vaultNamespace,
}

func getPostgreSQLTemplateData() (templateData, map[string]string) {
	return data, templateValues{
		"postgreSQLStatefulSetTemplate": postgreSQLStatefulSetTemplate,
		"postgreSQLServiceTemplate":     postgreSQLServiceTemplate,
	}
}

func getTemplateData() (templateData, map[string]string) {
	return data, templateValues{
		"secretTemplate":                secretTemplate,
		"deploymentTemplate":            deploymentTemplate,
		"triggerAuthenticationTemplate": triggerAuthenticationTemplate,
		"scaledObjectTemplate":          scaledObjectTemplate,
	}
}
