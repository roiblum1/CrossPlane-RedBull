package network

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
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
			kube:            mgr.GetClient(),
			usage:           resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ProviderConfigUsage{}),
			logger:          o.Logger,
			newHttpClientFn: httpclient.NewClient,
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
	kube            client.Client
	usage           resource.Tracker
	logger          logging.Logger
	newHttpClientFn func(log logging.Logger, timeout time.Duration, creds string) (httpclient.Client, error)
}

// Connect produces an ExternalClient for PortOrder resources.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.PortOrder)
	if !ok {
		return nil, errors.New(errNotPortOrder)
	}

	l := c.logger.WithValues("portOrder", cr.Name)

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	n := types.NamespacedName{Name: cr.GetProviderConfigReference().Name}
	if err := c.kube.Get(ctx, n, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	var creds string = ""
	if pc.Spec.Credentials.Source == xpv1.CredentialsSourceSecret {
		data, err := resource.CommonCredentialExtractor(ctx, pc.Spec.Credentials.Source, c.kube, pc.Spec.Credentials.CommonCredentialSelectors)
		if err != nil {
			return nil, errors.Wrap(err, errGetCreds)
		}
		creds = string(data)
	}

	// Parse credentials to get auth info
	config := struct {
		AuthType    string            `json:"authType,omitempty"`
		Credentials string            `json:"credentials,omitempty"`
		Headers     map[string]string `json:"headers,omitempty"`
		Timeout     *time.Duration    `json:"timeout,omitempty"`
	}{}

	if len(creds) > 0 {
		if err := json.Unmarshal([]byte(creds), &config); err != nil {
			return nil, errors.Wrap(err, "failed to parse credentials")
		}
	}

	// Default timeout
	timeout := 30 * time.Second
	if config.Timeout != nil {
		timeout = *config.Timeout
	}

	// Create HTTP client
	h, err := c.newHttpClientFn(l, timeout, creds)
	if err != nil {
		return nil, errors.Wrap(err, errNewClient)
	}

	return &external{
		client:         h,
		logger:         l,
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

	// Create the HTTP request using the client's SendRequest method
	bodyData := httpclient.Data{Encrypted: nil, Decrypted: string(body)}
	headersData := httpclient.Data{Encrypted: nil, Decrypted: headers}

	// Execute the request
	details, err := e.client.SendRequest(
		ctx,
		"POST",
		cr.Spec.ForProvider.APIEndpoint,
		bodyData,
		headersData,
		false, // InsecureSkipTLSVerify
	)

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to create order")
	}

	// Update status
	now := metav1.Now()
	cr.Status.AtProvider.LastRequestTime = &now
	cr.Status.AtProvider.LastResponseStatus = details.HttpResponse.StatusCode

	// Check if request was successful
	if details.HttpResponse.StatusCode != 201 && details.HttpResponse.StatusCode != 200 {
		return managed.ExternalCreation{}, errors.Errorf("unexpected status code: %d, body: %s",
			details.HttpResponse.StatusCode, string(details.HttpResponse.Body))
	}

	// Parse response to get order ID
	var orderResp OrderResponse
	if err := json.Unmarshal(details.HttpResponse.Body, &orderResp); err != nil {
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