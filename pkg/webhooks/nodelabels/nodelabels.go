package nodelabels

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
	WebhookName string = "node-labels-validation"
)

var (
	adminGroups = []string{"dedicated-admin"}

	scope = admissionregv1.AllScopes
	rules = []admissionregv1.RuleWithOperations{
		{
			Operations: []admissionregv1.OperationType{"UPDATE"},
			Rule: admissionregv1.Rule{
				APIGroups:   []string{""},
				APIVersions: []string{"*"},
				Resources:   []string{"nodes", "nodes/*"},
				Scope:       &scope,
			},
		},
	}
	log = logf.Log.WithName(WebhookName)
)

// NamespaceWebhook validates a Namespace change
type NodeLabelsWebhook struct {
	mu sync.Mutex
	s  runtime.Scheme
}

// TimeoutSeconds implements Webhook interface
func (s *NodeLabelsWebhook) TimeoutSeconds() int32 { return 2 }

// MatchPolicy implements Webhook interface
func (s *NodeLabelsWebhook) MatchPolicy() admissionregv1.MatchPolicyType {
	return admissionregv1.Equivalent
}

// Name implements Webhook interface
func (s *NodeLabelsWebhook) Name() string { return WebhookName }

// FailurePolicy implements Webhook interface
func (s *NodeLabelsWebhook) FailurePolicy() admissionregv1.FailurePolicyType {
	return admissionregv1.Ignore
}

// Rules implements Webhook interface
func (s *NodeLabelsWebhook) Rules() []admissionregv1.RuleWithOperations { return rules }

// GetURI implements Webhook interface
func (s *NodeLabelsWebhook) GetURI() string { return "/regularuser-validation" }

// SideEffects implements Webhook interface
func (s *NodeLabelsWebhook) SideEffects() admissionregv1.SideEffectClass {
	return admissionregv1.SideEffectClassNone
}

// Validate is the incoming request even valid?
func (s *NodeLabelsWebhook) Validate(req admissionctl.Request) bool {
	valid := true
	valid = valid && (req.UserInfo.Username != "")

	return valid
}

func (s *NodeLabelsWebhook) authorized(request admissionctl.Request) admissionctl.Response {
	var ret admissionctl.Response

	if request.AdmissionRequest.UserInfo.Username == "system:unauthenticated" {
		// This could highlight a significant problem with RBAC since an
		// unauthenticated user should have no permissions.
		log.Info("system:unauthenticated made a webhook request. Check RBAC rules", "request", request.AdmissionRequest)
		ret = admissionctl.Denied("Unauthenticated")
		ret.UID = request.AdmissionRequest.UID
		return ret
	}

	// Retrieve old and new node objects
	node := &corev1.Node{}
	oldNode := &corev1.Node{}

	err := json.Unmarshal(request.Object.Raw, node)
	if err != nil {
		errMsg := "Failed to Unmarshal node object"
		log.Error(err, errMsg)
		ret.UID = request.AdmissionRequest.UID
		ret = admissionctl.Denied(errMsg)
	}
	err = json.Unmarshal(request.OldObject.Raw, oldNode)
	if err != nil {
		errMsg := "Failed to Unmarshal old node object"
		log.Error(err, errMsg)
		ret.UID = request.AdmissionRequest.UID
		ret = admissionctl.Denied(errMsg)
	}

	// If a master or infra node is being changed - fail
	if val, ok := oldNode.Labels["type"]; ok {
		if val == "infra" || val == "master" {
			log.Info("Cannot edit master or infra nodes")
			ret.UID = request.AdmissionRequest.UID
			ret = admissionctl.Denied("UnauthorizedAction")
			return ret
		}
	}

	// If a worker node is losing its worker label - fail
	fail := false
	if val, ok := oldNode.Labels["type"]; ok {
		if val == "worker" {
			if val, ok := node.Labels["type"]; ok {
				if val != "worker" {
					fail = true
				}
			} else {
				fail = true
			}
		}
	}
	if fail {
		log.Info("Cannot overwrite worker node label")
		ret.UID = request.AdmissionRequest.UID
		ret = admissionctl.Denied("UnauthorizedAction")
		return ret
	}

	// If a new node is given a master or infra label - fail
	if val, ok := oldNode.Labels["type"]; ok {
		if val != "master" && val != "infra" {
			if val, ok := node.Labels["type"]; ok {
				if val == "master" || val == "infra" {
					log.Info("Cannot assign new node a master or infra label")
					ret.UID = request.AdmissionRequest.UID
					ret = admissionctl.Denied("UnauthorizedAction")
					return ret
				}
			}
		}
	}

	// Allow Access
	ret = admissionctl.Allowed("New label does not infringe on node properties")
	ret.UID = request.AdmissionRequest.UID
	return ret
}

// HandleRequest hndles the incoming HTTP request
func (s *NodeLabelsWebhook) HandleRequest(w http.ResponseWriter, r *http.Request) {

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
		resp := admissionctl.Errored(http.StatusBadRequest, fmt.Errorf("Could not parse Namespace from request"))
		resp.UID = request.AdmissionRequest.UID
		responsehelper.SendResponse(w, resp)

		return
	}
	// should the request be authorized?

	responsehelper.SendResponse(w, s.authorized(request))

}

// NewWebhook creates a new webhook
func NewWebhook() *NodeLabelsWebhook {
	scheme := runtime.NewScheme()
	v1beta1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)

	return &NodeLabelsWebhook{
		s: *scheme,
	}
}
