package tools

import (
	"context"
	"fmt"
	"os"
	"time"

	"cloud.google.com/go/bigquery"
	mcpgrafana "github.com/grafana/mcp-grafana"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"google.golang.org/api/iterator"
)

// Client represents a New Relic API client
type GcloudBillingClient struct {
	BigqueryClient      *bigquery.Client
	DetailedTableName   string // Full BigQuery table name including project and dataset, with backticks
	BigqueryDatasetID   string // Just the dataset ID, in case a tool needs to reference the dataset specifically
	GCPProjectID        string // Added for tools that need the project ID (e.g., Recommender API)
	GCPBillingAccountID string // Added for tools that need the billing account ID (e.g., Budget API)
}

// NewBillingClient creates a new
func NewBillingClient(ctx context.Context) (*GcloudBillingClient, error) {
	projectID := os.Getenv("GCP_PROJECT_ID")
	billingAccountID := os.Getenv("GCP_BILLING_ACCOUNT_ID")
	billingAccountDataset := os.Getenv("BIGQUERY_BILLING_DATASET_ID")
	billingAccountTableName := os.Getenv("BIGQUERY_DETAILED_TABLE_NAME")
	if projectID == "" {
		return nil, fmt.Errorf("GCP_PROJECT_ID environment variable is not set")
	}

	bqClient, err := bigquery.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("creating BigQuery client: %w", err)
	}

	return &GcloudBillingClient{
		GCPProjectID:        projectID,
		GCPBillingAccountID: billingAccountID,
		BigqueryClient:      bqClient,
		BigqueryDatasetID:   billingAccountDataset,
		DetailedTableName:   billingAccountTableName,
	}, nil
}

type GetCostSummaryParams struct {
	StartDate string `json:"start_date" jsonschema:"required,description=The start date of the cost period in YYYY-MM-DD format"`
	EndDate   string `json:"end_date" jsonschema:"required,description=The end date of the cost period in YYYY-MM-DD format"`
	GroupBy   string `json:"group_by" jsonschema:"required,description=The dimension to group costs by (e.g.,'service', 'project','sku', 'invoice_month'). Default is'service'."`
}

// GetCostSummaryTool retrieves a summary of GCP costs for a given period, grouped by service, project, or SKU.
func GetCostSummaryTool(ctx context.Context, args GetCostSummaryParams) (any, error) {
	client, err := NewBillingClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating Google Cloud Billing client: %w", err)
	}
	if args.StartDate == "" {
		return nil, fmt.Errorf("missing or invalid 'start_date'")
	}
	if args.EndDate == "" {
		return nil, fmt.Errorf("missing or invalid 'end_date'")
	}
	if args.GroupBy == "" {
		args.GroupBy = "service"
	}
	startDate, err := time.Parse("2006-01-02", args.StartDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start_date format: %w", err)
	}
	endDate, err := time.Parse("2006-01-02", args.EndDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end_date format: %w", err)
	}

	// Basic input validation for group_by
	allowedGroupBy := map[string]string{
		"service":       "service.description",
		"project":       "project.id",
		"sku":           "sku.description",
		"invoice_month": "invoice.month",
	}
	groupColumn, found := allowedGroupBy[args.GroupBy]
	if !found {
		groupColumn = allowedGroupBy["service"] // Default to service if invalid or not provided
		if args.GroupBy != "" {                 // Only log if user provided an invalid value, not if it was empty
			fmt.Printf("Warning: Invalid group_by dimension '%s'. Defaulting to 'service'.\n", args.GroupBy)
		}
	}

	query := fmt.Sprintf(`
	SELECT
		%s AS group_dimension,
		SUM(cost) AS total_cost
	FROM
		%s
	WHERE
		_PARTITIONTIME BETWEEN TIMESTAMP('%s') AND TIMESTAMP('%s')
	GROUP BY
		group_dimension
	ORDER BY
		total_cost DESC
	`, groupColumn, client.DetailedTableName, startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	fmt.Printf("Executing BigQuery query for get_cost_summary:\n%s\n", query) // For debugging

	job := client.BigqueryClient.Query(query)
	it, err := job.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("BigQuery query failed: %w", err)
	}

	var summary []map[string]interface{}
	for {
		var row []bigquery.Value
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading BigQuery row: %w", err)
		}

		// Assuming order: group_dimension, total_cost
		if len(row) < 2 {
			return nil, fmt.Errorf("unexpected number of columns in BigQuery result")
		}

		summary = append(summary, map[string]interface{}{
			"dimension": row[0],
			"cost":      fmt.Sprintf("%.2f", row[1]), // Format cost to 2 decimal places for display
		})
	}

	return map[string]interface{}{"summary": summary}, nil
}

// GetAppHealth is a tool for retrieving application health data from New Relic
var GetGCPCostSummary = mcpgrafana.MustTool(
	"get_gcp_cost_summary",
	"Get GCP cost summary for a given period, grouped by service, project, or SKU",
	GetCostSummaryTool,
	mcp.WithTitleAnnotation("Get GCP Cost Summary"),
	mcp.WithIdempotentHintAnnotation(true),
	mcp.WithReadOnlyHintAnnotation(true),
)

// AddGoogleCloudBillingTools registers all Google Cloud Billing tools with the MCP server
func AddGoogleCloudBillingTools(mcp *server.MCPServer) {
	GetGCPCostSummary.Register(mcp)
}
