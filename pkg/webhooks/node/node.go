package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	responsehelper "github.com/openshift/managed-cluster-validating-webhooks/pkg/helpers"
	"github.com/openshift/managed-cluster-validating-webhooks/pkg/webhooks/utils"
	"k8s.io/api/admission/v1beta1"
	admissionregv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	admissionctl "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

const (
	// WebhookName name
	WebhookName string = "node-validation"

	// Labels
	masterLabel string = "node-role.kubernetes.io/master"
	infraLabel  string = "node-role.kubernetes.io"
	workerLabel string = "node-role.kubernetes.io/worker"
)

var (
	adminGroups = []string{"dedicated-admins"}
	scope       = admissionregv1.AllScopes
	rules       = []admissionregv1.RuleWithOperations{
		{
			Operations: []admissionregv1.OperationType{admissionregv1.Update, admissionregv1.Delete},
			Rule: admissionregv1.Rule{
				APIGroups:   []string{"*"},
				APIVersions: []string{"*"},
				Resources:   []string{"nodes", "nodes/*"},
				Scope:       &scope,
			},
		},
	}
	log = logf.Log.WithName(WebhookName)
)

// LabelsWebhook validates label changes on nodes
type LabelsWebhook struct {
	mu sync.Mutex
	s  runtime.Scheme
}

// TimeoutSeconds implements Webhook interface
func (s *LabelsWebhook) TimeoutSeconds() int32 { return 1 }

// MatchPolicy implements Webhook interface
func (s *LabelsWebhook) MatchPolicy() admissionregv1.MatchPolicyType {
	return admissionregv1.Exact // TODO: maybe issue lies here
}

// Name implements Webhook interface
func (s *LabelsWebhook) Name() string { return WebhookName }

// FailurePolicy implements Webhook interface
func (s *LabelsWebhook) FailurePolicy() admissionregv1.FailurePolicyType {
	return admissionregv1.Ignore
}

// Rules implements Webhook interface
func (s *LabelsWebhook) Rules() []admissionregv1.RuleWithOperations { return rules }

// GetURI implements Webhook interface
func (s *LabelsWebhook) GetURI() string { return "/" + WebhookName }

// SideEffects implements Webhook interface
func (s *LabelsWebhook) SideEffects() admissionregv1.SideEffectClass {
	return admissionregv1.SideEffectClassNone
}

// Validate is the incoming request even valid?
func (s *LabelsWebhook) Validate(req admissionctl.Request) bool {

	// Check if incoming request is a node request
	// Retrieve old and new node objects
	node := &corev1.Node{}
	oldNode := &corev1.Node{}

	err := json.Unmarshal(req.Object.Raw, node)
	if err != nil {
		errMsg := "Failed to Unmarshal node object"
		log.Error(err, errMsg)
		return false
	}
	err = json.Unmarshal(req.OldObject.Raw, oldNode)
	if err != nil {
		errMsg := "Failed to Unmarshal old node object"
		log.Error(err, errMsg)
		return false
	}
	return true
}

func (s *LabelsWebhook) authorized(request admissionctl.Request) admissionctl.Response {
	var ret admissionctl.Response

	if request.AdmissionRequest.UserInfo.Username == "system:unauthenticated" {
		// This could highlight a significant problem with RBAC since an
		// unauthenticated user should have no permissions.
		log.Info("system:unauthenticated made a webhook request. Check RBAC rules", "request", request.AdmissionRequest)
		ret = admissionctl.Denied("Unauthenticated")
		ret.UID = request.AdmissionRequest.UID
		return ret
	}

	// Check that the current user is a dedicated admin
	for _, userGroup := range request.UserInfo.Groups {

		// Retrieve old and new node objects
		node := &corev1.Node{}
		oldNode := &corev1.Node{}

		err := json.Unmarshal(request.Object.Raw, node)
		if err != nil {
			errMsg := "Failed to Unmarshal node object"
			log.Error(err, errMsg)
			ret = admissionctl.Denied("UnauthorizedAction")
			return ret
		}
		err = json.Unmarshal(request.OldObject.Raw, oldNode)
		if err != nil {
			errMsg := "Failed to Unmarshal old node object"
			log.Error(err, errMsg)
			ret = admissionctl.Denied("UnauthorizedAction")
			return ret
		}

		log.Info(fmt.Sprintf("request: %s", request.UserInfo.Username))
		log.Info(fmt.Sprintf("new: %v", node.Labels))
		log.Info(fmt.Sprintf("old: %v", oldNode.Labels))

		if contains(adminGroups, userGroup) {

			// Fail on none worker nodes
			if _, ok := oldNode.Labels[workerLabel]; !ok {
				log.Info("cannot edit non-worker node")
				ret.UID = request.AdmissionRequest.UID
				ret = admissionctl.Denied("UnauthorizedAction")
				return ret
			}

			// Fail on infra,worker nodes
			if val, ok := oldNode.Labels[infraLabel]; ok && val == "infra" {
				log.Info("cannot edit non-worker node")
				ret.UID = request.AdmissionRequest.UID
				ret = admissionctl.Denied("UnauthorizedAction")
				return ret
			}

			// Do not allow worker node type to change to master
			if _, ok := node.Labels[masterLabel]; ok {
				log.Info("cannot change worker node to master")
				ret.UID = request.AdmissionRequest.UID
				ret = admissionctl.Denied("UnauthorizedAction")
				return ret
			}

			// Do not allow worker node type to change to infra
			if _, ok := node.Labels[infraLabel]; ok {
				log.Info("cannot change worker node to infra")
				ret.UID = request.AdmissionRequest.UID
				ret = admissionctl.Denied("UnauthorizedAction")
				return ret
			}

			// Fail on removed worker node label
			if _, ok := node.Labels[workerLabel]; !ok {
				log.Info("cannot remove worker node label from worker node")
				ret.UID = request.AdmissionRequest.UID
				ret = admissionctl.Denied("UnauthorizedAction")
				return ret
			}
		}
	}
	// Allow Operation
	msg := "New label does not infringe on node properties"
	log.Info(msg)
	ret = admissionctl.Allowed(msg)
	ret.UID = request.AdmissionRequest.UID
	return ret
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

// HandleRequest handles the incoming HTTP request
func (s *LabelsWebhook) HandleRequest(w http.ResponseWriter, r *http.Request) {

	s.mu.Lock()
	defer s.mu.Unlock()
	request, _, err := utils.ParseHTTPRequest(r)
	if err != nil {
		log.Error(err, "Error parsing HTTP Request Body")
		responsehelper.SendResponse(w, admissionctl.Errored(http.StatusBadRequest, err))
		return
	}
	// Is this a valid request?
	if !s.Validate(request) {
		resp := admissionctl.Errored(http.StatusBadRequest, fmt.Errorf("Invalid request"))
		resp.UID = request.AdmissionRequest.UID
		responsehelper.SendResponse(w, resp)
		return
	}
	// should the request be authorized?
	responsehelper.SendResponse(w, s.authorized(request))

}

// NewWebhook creates a new webhook
func NewWebhook() *LabelsWebhook {
	scheme := runtime.NewScheme()
	v1beta1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)

	return &LabelsWebhook{
		s: *scheme,
	}
}
