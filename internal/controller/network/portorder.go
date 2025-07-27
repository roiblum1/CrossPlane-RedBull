package network

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/connection"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane/provider-http/apis/network/v1alpha1"
	apisv1alpha1 "github.com/crossplane/provider-http/apis/v1alpha1"
	httpclient "github.com/crossplane/provider-http/internal/clients/http"
	"github.com/crossplane/provider-http/internal/features"
)

const (
	errNotPortOrder = "managed resource is not a PortOrder custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errGetCreds     = "cannot get credentials"

	errNewClient = "cannot create new HTTP client"
	errMarshal   = "cannot marshal request body"
	errUnmarshal = "cannot unmarshal response"
)

// OrderRequest represents the API request format
type OrderRequest struct {
	Order OrderPayload `json:"order"`
}

// OrderPayload represents the order details
type OrderPayload struct {
	Source      string      `json:"source"`
	Destination string      `json:"destination"`
	Ports       []PortEntry `json:"ports"`
}

// PortEntry represents a port in the API format
type PortEntry struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
}

// OrderResponse represents the API response format
type OrderResponse struct {
	OrderID string `json:"orderId"`
	Status  string `json:"status"`
}

// Setup adds a controller that reconciles PortOrder managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.PortOrderGroupKind)

	cps := []managed.ConnectionPublisher{managed.NewAPISecretPublisher(mgr.GetClient(), mgr.GetScheme())}
	if o.Features.Enabled(features.EnableAlphaExternalSecretStores) {
		cps = append(cps, connection.NewDetailsManager(mgr.GetClient(), apisv1alpha1.StoreConfigGroupVersionKind))
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.PortOrderGroupVersionKind),
		managed.WithExternalConnecter(&connector{
			kube:   mgr.GetClient(),
			usage:  resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			logger: logging.NewLogrLogger(mgr.GetLogger().WithName("portorder")),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		managed.WithConnectionPublishers(cps...),
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.PortOrder{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube   client.Client
	usage  resource.Tracker
	logger logging.Logger
}

// Connect produces an ExternalClient for PortOrder resources.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.PortOrder)
	if !ok {
		return nil, errors.New(errNotPortOrder)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	cd := pc.Spec.Credentials
	data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	// Parse credentials to get auth info
	config := struct {
		AuthType    string            `json:"authType,omitempty"`
		Credentials string            `json:"credentials,omitempty"`
		Headers     map[string]string `json:"headers,omitempty"`
	}{}

	if len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, errors.Wrap(err, "failed to parse credentials")
		}
	}

	// Create HTTP client with authentication
	opts := []httpclient.ClientOption{
		httpclient.WithMiddleware(httpclient.LoggingMiddleware()),
		httpclient.WithMiddleware(httpclient.JSONMiddleware()),
	}

	if config.AuthType != "" && config.Credentials != "" {
		opts = append(opts, httpclient.WithMiddleware(
			httpclient.AuthMiddleware(config.AuthType, config.Credentials),
		))
	}

	httpClient := httpclient.NewClient(opts...)

	return &external{
		client:         httpClient,
		logger:         c.logger,
		defaultHeaders: config.Headers,
	}, nil
}

// external manages the external API operations for PortOrder resources.
type external struct {
	client         httpclient.Client
	logger         logging.Logger
	defaultHeaders map[string]string
}

func (e *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.PortOrder)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotPortOrder)
	}

	// If we don't have an order ID, the resource doesn't exist externally
	if cr.Status.AtProvider.OrderID == "" {
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil
	}

	// Check the status of the existing order
	// For now, we'll consider the resource to exist if we have an order ID
	// In a real implementation, you might want to GET the order status from the API
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true, // Port orders are typically one-time requests
	}, nil
}

func (e *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.PortOrder)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotPortOrder)
	}

	e.logger.Debug("Creating PortOrder", "name", cr.GetName())

	// Build the request body in the format the API expects
	orderReq := OrderRequest{
		Order: OrderPayload{
			Source:      cr.Spec.ForProvider.Source,
			Destination: cr.Spec.ForProvider.Destination,
			Ports:       e.convertPorts(cr.Spec.ForProvider.Ports),
		},
	}

	// Marshal the request body
	body, err := json.Marshal(orderReq)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errMarshal)
	}

	// Prepare headers
	headers := make(map[string]string)
	for k, v := range e.defaultHeaders {
		headers[k] = v
	}
	headers["X-Request-ID"] = fmt.Sprintf("crossplane-%s", cr.GetUID())

	// Create the HTTP request
	req := &httpclient.Request{
		URL:     cr.Spec.ForProvider.APIEndpoint,
		Method:  "POST",
		Headers: headers,
		Body:    body,
		RetryPolicy: &httpclient.RetryPolicy{
			MaxAttempts:    3,
			BackoffSeconds: 2,
		},
	}

	// Execute the request
	resp, err := e.client.Do(ctx, req)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to create order")
	}

	// Update status
	now := meta.Now()
	cr.Status.AtProvider.LastRequestTime = &now
	cr.Status.AtProvider.LastResponseStatus = resp.StatusCode

	// Check if request was successful
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		return managed.ExternalCreation{}, errors.Errorf("unexpected status code: %d, body: %s", 
			resp.StatusCode, string(resp.Body))
	}

	// Parse response to get order ID
	var orderResp OrderResponse
	if err := json.Unmarshal(resp.Body, &orderResp); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errUnmarshal)
	}

	// Update status with order details
	cr.Status.AtProvider.OrderID = orderResp.OrderID
	cr.Status.AtProvider.Status = orderResp.Status

	// Set external name to order ID
	meta.SetExternalName(cr, orderResp.OrderID)

	return managed.ExternalCreation{}, nil
}

func (e *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// Port orders are typically immutable once created
	// In a real implementation, you might support status updates or modifications
	return managed.ExternalUpdate{}, nil
}

func (e *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.PortOrder)
	if !ok {
		return errors.New(errNotPortOrder)
	}

	e.logger.Debug("Deleting PortOrder", "name", cr.GetName())

	// In a real implementation, you might send a DELETE request to cancel the order
	// For now, we'll just consider it deleted
	return nil
}

// convertPorts converts from our CRD format to the API format
func (e *external) convertPorts(ports []v1alpha1.PortParameters) []PortEntry {
	result := make([]PortEntry, len(ports))
	for i, p := range ports {
		result[i] = PortEntry{
			Protocol: strings.ToUpper(p.Type),
			Port:     p.Number,
		}
	}
	return result
}