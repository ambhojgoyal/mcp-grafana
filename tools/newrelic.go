package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const nrAPI = "https://api.newrelic.com/graphql"

// Client represents a New Relic API client
type NRClient struct {
	httpClient *http.Client
	apiKey     string
}

// NewNRClient creates a new New Relic client
func NewNRClient(ctx context.Context, apiKeyParam string) (*NRClient, error) {
	apiKey := apiKeyParam
	
	// Fall back to environment variable if parameter is empty
	if apiKey == "" {
		apiKey = os.Getenv("NEW_RELIC_API_KEY")
	}
	
	if apiKey == "" {
		return nil, fmt.Errorf("New Relic API key not provided and NEW_RELIC_API_KEY environment variable is not set")
	}

	return &NRClient{
		httpClient: http.DefaultClient,
		apiKey:     apiKey,
	}, nil
}

// RunQuery executes a GraphQL query against the New Relic API
func (c *NRClient) RunQuery(ctx context.Context, query string) (any, error) {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, fmt.Errorf("error marshaling query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", nrAPI, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("error creating HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("API-Key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making request to New Relic API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("New Relic API returned non-200 status: %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading response body: %w", err)
	}

	var result any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("error unmarshaling response: %w", err)
	}

	return result, nil
}

// ========== Tool 1: Application Health Summary ==========

type GetAppHealthParams struct {
	AppName   string `json:"app_name" jsonschema:"required,description=The name of the application to get health data for"`
	AccountID string `json:"account_id" jsonschema:"required,description=The New Relic account ID"`
	ApiKey    string `json:"api_key,omitempty" jsonschema:"description=New Relic API Key. If not provided, will use the NEW_RELIC_API_KEY environment variable"`
}

func getAppHealth(ctx context.Context, args GetAppHealthParams) (any, error) {
	client, err := NewNRClient(ctx, args.ApiKey)
	if err != nil {
		return nil, fmt.Errorf("creating New Relic client: %w", err)
	}

	// First, search for the application by name to get metadata
	entitySearchQuery := fmt.Sprintf(`
	{
		actor {
			entitySearch(queryBuilder: {domain: APM, type: APPLICATION, name: "%s"}) {
				results {
					entities {
						guid
						alertSeverity
					}
				}
			}
		}
	}`, args.AppName)

	entityResult, err := client.RunQuery(ctx, entitySearchQuery)
	if err != nil {
		return nil, fmt.Errorf("searching for application entity: %w", err)
	}

	// Now use NRQL queries to get the health metrics
	metricsQuery := fmt.Sprintf(`
	{
		actor {
			account(id: %s) {
				nrql(query: "SELECT latest(apdexScore) as 'apdexScore', latest(errorRate) as 'errorRate', latest(throughput) as 'throughput', latest(responseTime) as 'responseTime' FROM ApplicationSummary WHERE appName = '%s'") {
					results
				}
			}
		}
	}`, args.AccountID, args.AppName)

	metricsResult, err := client.RunQuery(ctx, metricsQuery)
	if err != nil {
		return nil, fmt.Errorf("getting application metrics: %w", err)
	}

	// Combine the results
	return map[string]interface{}{
		"entityInfo": entityResult,
		"metrics": metricsResult,
	}, nil
}

// GetAppHealth is a tool for retrieving application health data from New Relic
var GetAppHealth = mcpgrafana.MustTool(
	"get_app_health",
	"Get New Relic APM health summary for an application, including error rate, apdex score, throughput, and response time",
	getAppHealth,
	mcp.WithTitleAnnotation("Get Application Health"),
	mcp.WithIdempotentHintAnnotation(true),
	mcp.WithReadOnlyHintAnnotation(true),
)

// ========== Tool 2: Top 5 Slow Transactions ==========

type GetSlowTransactionsParams struct {
	AppName   string `json:"app_name" jsonschema:"required,description=The name of the application to get slow transactions for"`
	AccountID string `json:"account_id" jsonschema:"required,description=The New Relic account ID"`
	Limit     int    `json:"limit,omitempty" jsonschema:"description=The maximum number of transactions to return (default: 5)"`
	ApiKey    string `json:"api_key,omitempty" jsonschema:"description=New Relic API Key. If not provided, will use the NEW_RELIC_API_KEY environment variable"`
}

func getSlowTransactions(ctx context.Context, args GetSlowTransactionsParams) (any, error) {
	client, err := NewNRClient(ctx, args.ApiKey)
	if err != nil {
		return nil, fmt.Errorf("creating New Relic client: %w", err)
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 5
	}

	query := fmt.Sprintf(`
	{
		actor {
			account(id: %s) {
				nrql(query: "SELECT average(duration) FROM Transaction FACET name LIMIT %d WHERE appName = '%s'") {
					results
				}
			}
		}
	}`, args.AccountID, limit, args.AppName)

	return client.RunQuery(ctx, query)
}

// GetSlowTransactions is a tool for retrieving the slowest transactions from New Relic
var GetSlowTransactions = mcpgrafana.MustTool(
	"get_slow_transactions",
	"List top slow transactions for a New Relic application, showing average duration for each transaction name",
	getSlowTransactions,
	mcp.WithTitleAnnotation("Get Slow Transactions"),
	mcp.WithIdempotentHintAnnotation(true),
	mcp.WithReadOnlyHintAnnotation(true),
)

// ========== Tool 3: Custom NRQL Query ==========

type RunCustomNRQLParams struct {
	AccountID string `json:"account_id" jsonschema:"required,description=The New Relic account ID"`
	NRQL      string `json:"nrql" jsonschema:"required,description=The NRQL query to execute"`
	ApiKey    string `json:"api_key,omitempty" jsonschema:"description=New Relic API Key. If not provided, will use the NEW_RELIC_API_KEY environment variable"`
}

func runCustomNRQL(ctx context.Context, args RunCustomNRQLParams) (any, error) {
	client, err := NewNRClient(ctx, args.ApiKey)
	if err != nil {
		return nil, fmt.Errorf("creating New Relic client: %w", err)
	}

	query := fmt.Sprintf(`
	{
		actor {
			account(id: %s) {
				nrql(query: "%s") {
					results
				}
			}
		}
	}`, args.AccountID, args.NRQL)

	return client.RunQuery(ctx, query)
}

// RunCustomNRQL is a tool for executing custom NRQL queries in New Relic
var RunCustomNRQL = mcpgrafana.MustTool(
	"run_custom_nrql",
	"Run a custom NRQL query against New Relic and return the results",
	runCustomNRQL,
	mcp.WithTitleAnnotation("Run Custom NRQL Query"),
	mcp.WithIdempotentHintAnnotation(true),
	mcp.WithReadOnlyHintAnnotation(true),
)

// AddNewRelicTools registers all New Relic tools with the MCP server
func AddNewRelicTools(mcp *server.MCPServer) {
	GetAppHealth.Register(mcp)
	GetSlowTransactions.Register(mcp)
	RunCustomNRQL.Register(mcp)
}
