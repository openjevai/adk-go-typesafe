package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the TypeSafe API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel follows TypeSafe's current Jev model alias.
	DefaultModel = "jev-latest"

	// OpenjevBaseURL is the OpenJEV community gateway API root.
	OpenjevBaseURL = "https://api.openjev.sh"
	// OpenjevModel is the model alias on the OpenJEV gateway.
	OpenjevModel = "openjev"
	// OpenjevAPIKeyEnv is the environment variable read for the OpenJEV key.
	OpenjevAPIKeyEnv = "OPENJEV_API_KEY"
	// ProviderEnv selects the Jev provider when Options.Provider is empty.
	ProviderEnv = "JEV_PROVIDER"

	defaultTimeout   = 10 * time.Second
	maxResponseBytes = 8 << 20
)

// Provider constants for Options.Provider.
const (
	// ProviderTypeSafe uses the TypeSafe direct API (default, unchanged behaviour).
	ProviderTypeSafe = "typesafe"
	// ProviderOpenjev uses the OpenJEV community gateway.
	ProviderOpenjev = "openjev"
)

// Options configures New. The zero value uses TYPESAFE_API_KEY, the public API,
// jev-latest, and a ten-second HTTP timeout.
//
// OpenJEV is an optional alternative provider — a free community gateway to
// the same Jev model. Set Provider to "openjev" (or JEV_PROVIDER=openjev) to use
// it; otherwise TypeSafe stays the default. When Provider is empty, TypeSafe is
// used if TYPESAFE_API_KEY (or Options.APIKey) is set; OpenJEV is used
// automatically if only OPENJEV_API_KEY is set.
type Options struct {
	APIKey string
	Model  string
	// BaseURL is the API root, without /v1/systemone.
	BaseURL string
	// Provider selects the Jev provider: "typesafe" (default) or "openjev".
	// Empty selects TypeSafe when a TypeSafe key is available, otherwise
	// OpenJEV when OPENJEV_API_KEY is set. JEV_PROVIDER overrides the empty
	// value at construction time.
	Provider string
	// OpenjevAPIKey is the OpenJEV API key, used when Provider is "openjev".
	// Empty reads OPENJEV_API_KEY from the environment.
	OpenjevAPIKey string
	// HTTPClient overrides the default transport and timeout. Redirects are
	// disabled unless this client supplies its own CheckRedirect policy.
	HTTPClient *http.Client
	// Retry enables bounded retries for HTTP 429, 503, and 529. Nil disables retries.
	Retry *RetryPolicy
}

// Evaluator is the evaluation boundary used by ADK adapters.
type Evaluator interface {
	Evaluate(context.Context, *Request) (*Response, error)
}

// Client calls TypeSafe's System One API. It is safe for concurrent use.
// It does not implement ADK's generative model.LLM interface.
type Client struct {
	apiKey   string
	model    string
	endpoint string
	http     *http.Client
	retry    RetryPolicy
}

// New creates a client. A nil options pointer uses the defaults.
func New(opts *Options) (*Client, error) {
	var cfg Options
	if opts != nil {
		cfg = *opts
	}
	provider := strings.TrimSpace(strings.ToLower(cfg.Provider))
	if provider == "" {
		provider = strings.TrimSpace(strings.ToLower(os.Getenv(ProviderEnv)))
	}

	// Determine which keys are available.
	typesafeKey := strings.TrimSpace(cfg.APIKey)
	if typesafeKey == "" {
		typesafeKey = strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	}
	openjevKey := strings.TrimSpace(cfg.OpenjevAPIKey)
	if openjevKey == "" {
		openjevKey = strings.TrimSpace(os.Getenv(OpenjevAPIKeyEnv))
	}

	switch provider {
	case ProviderOpenjev:
		// Explicit OpenJEV selection.
		if openjevKey == "" {
			return nil, errors.New("typesafe: OpenJEV provider selected but no API key found; set OPENJEV_API_KEY or Options.OpenjevAPIKey")
		}
		cfg.APIKey = openjevKey
		if strings.TrimSpace(cfg.Model) == "" {
			cfg.Model = OpenjevModel
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = OpenjevBaseURL
		}
	case ProviderTypeSafe:
		// Explicit TypeSafe selection (default behaviour).
		if typesafeKey == "" {
			return nil, errors.New("typesafe: TypeSafe provider selected but no API key found; set TYPESAFE_API_KEY or Options.APIKey")
		}
		cfg.APIKey = typesafeKey
		if strings.TrimSpace(cfg.Model) == "" {
			cfg.Model = DefaultModel
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultBaseURL
		}
	default:
		// Auto: TypeSafe first (default unchanged), then OpenJEV.
		if typesafeKey != "" {
			cfg.APIKey = typesafeKey
			if strings.TrimSpace(cfg.Model) == "" {
				cfg.Model = DefaultModel
			}
			if cfg.BaseURL == "" {
				cfg.BaseURL = DefaultBaseURL
			}
		} else if openjevKey != "" {
			cfg.APIKey = openjevKey
			if strings.TrimSpace(cfg.Model) == "" {
				cfg.Model = OpenjevModel
			}
			if cfg.BaseURL == "" {
				cfg.BaseURL = OpenjevBaseURL
			}
		} else {
			return nil, errors.New("typesafe: API key is required; set TYPESAFE_API_KEY (or Options.APIKey) for TypeSafe, or OPENJEV_API_KEY for OpenJEV, or set Provider explicitly")
		}
	}

	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if cfg.APIKey == "" {
		return nil, errors.New("typesafe: API key is required")
	}
	for _, char := range cfg.APIKey {
		if char <= ' ' || char >= 127 {
			return nil, errors.New("typesafe: API key must contain only printable ASCII without whitespace")
		}
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("typesafe: base URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	client := http.Client{Timeout: defaultTimeout}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	if client.CheckRedirect == nil {
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	}
	retry, err := retryPolicy(cfg.Retry)
	if err != nil {
		return nil, err
	}
	return &Client{
		apiKey: cfg.APIKey, model: cfg.Model,
		endpoint: base.JoinPath("v1", "systemone").String(), http: &client, retry: retry,
	}, nil
}

// APIError reports an unsuccessful HTTP response. Body contains the provider's
// diagnostic response and may include submitted content; Error omits it so that
// ordinary error logs do not disclose that content. RetryAfter is the raw header.
type APIError struct {
	StatusCode int
	Body       string
	RetryAfter string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("typesafe: API returned HTTP %d", e.StatusCode)
}

// Evaluate submits one request. It preserves probabilities, confidence, rubric
// legends, model identity, and usage. Retries are disabled unless Options.Retry
// is configured. Context cancellation covers HTTP calls and retry delays.
// Responses larger than 8 MiB are rejected.
func (c *Client) Evaluate(ctx context.Context, req *Request) (*Response, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	input := *req
	if input.Model == "" {
		input.Model = c.model
	}
	body, err := json.Marshal(&input)
	if err != nil {
		return nil, fmt.Errorf("typesafe: encode request: %w", err)
	}
	data, err := c.sendWithRetry(ctx, body)
	if err != nil {
		return nil, err
	}
	var response Response
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("typesafe: decode response: %w", err)
	}
	if err := validateAnswers(&input, &response); err != nil {
		return nil, fmt.Errorf("typesafe: invalid response: %w", err)
	}
	return &response, nil
}

func (c *Client) send(ctx context.Context, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("typesafe: create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("typesafe: send request: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("typesafe: read response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("typesafe: response exceeds 8 MiB limit")
	}
	if res.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: res.StatusCode, Body: string(data), RetryAfter: res.Header.Get("Retry-After")}
	}
	return data, nil
}
