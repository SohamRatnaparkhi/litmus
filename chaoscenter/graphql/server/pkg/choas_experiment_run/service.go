package chaos_experiment_run

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/litmuschaos/litmus/chaoscenter/graphql/server/pkg/chaos_infrastructure"

	dbChaosExperimentRun "github.com/litmuschaos/litmus/chaoscenter/graphql/server/pkg/database/mongodb/chaos_experiment_run"

	"github.com/litmuschaos/litmus/chaoscenter/graphql/server/graph/model"
	store "github.com/litmuschaos/litmus/chaoscenter/graphql/server/pkg/data-store"
	dbChaosExperiment "github.com/litmuschaos/litmus/chaoscenter/graphql/server/pkg/database/mongodb/chaos_experiment"

	dbChaosInfra "github.com/litmuschaos/litmus/chaoscenter/graphql/server/pkg/database/mongodb/chaos_infrastructure"

	"github.com/litmuschaos/litmus/chaoscenter/graphql/server/utils"

	"go.mongodb.org/mongo-driver/bson"
)

type Service interface {
	ProcessExperimentRunDelete(ctx context.Context, query bson.D, workflowRunID *string, experimentRun dbChaosExperimentRun.ChaosExperimentRun, workflow dbChaosExperiment.ChaosExperimentRequest, username string, r *store.StateData) error
	ProcessCompletedExperimentRun(execData ExecutionData, wfID string, runID string) (ExperimentRunMetrics, error)
}

// chaosWorkflowService is the implementation of the chaos workflow service
type chaosExperimentRunService struct {
	chaosExperimentOperator     *dbChaosExperiment.Operator
	chaosInfrastructureOperator *dbChaosInfra.Operator
	chaosExperimentRunOperator  *dbChaosExperimentRun.Operator
}

// NewChaosExperimentRunService returns a new instance of the chaos workflow run service
func NewChaosExperimentRunService(chaosWorkflowOperator *dbChaosExperiment.Operator, clusterOperator *dbChaosInfra.Operator, chaosExperimentRunOperator *dbChaosExperimentRun.Operator) Service {
	return &chaosExperimentRunService{
		chaosExperimentOperator:     chaosWorkflowOperator,
		chaosInfrastructureOperator: clusterOperator,
		chaosExperimentRunOperator:  chaosExperimentRunOperator,
	}
}

// ProcessExperimentRunDelete deletes a workflow entry and updates the database
func (c *chaosExperimentRunService) ProcessExperimentRunDelete(ctx context.Context, query bson.D, workflowRunID *string, experimentRun dbChaosExperimentRun.ChaosExperimentRun, workflow dbChaosExperiment.ChaosExperimentRequest, username string, r *store.StateData) error {
	update := bson.D{
		{"$set", bson.D{
			{"is_removed", experimentRun.IsRemoved},
			{"updated_at", time.Now().UnixMilli()},
			{"updated_by", username},
		}},
	}

	err := c.chaosExperimentRunOperator.UpdateExperimentRunWithQuery(ctx, query, update)
	if err != nil {
		return err
	}
	if r != nil {
		chaos_infrastructure.SendExperimentToSubscriber(experimentRun.ProjectID, &model.ChaosExperimentRequest{
			InfraID: workflow.InfraID,
		}, &username, workflowRunID, "workflow_run_delete", r)
	}

	return nil
}

// ProcessCompletedExperimentRun calculates the Resiliency Score and returns the updated ExecutionData
func (c *chaosExperimentRunService) ProcessCompletedExperimentRun(execData ExecutionData, wfID string, runID string) (ExperimentRunMetrics, error) {
	var weightSum, totalTestResult = 0, 0
	var result ExperimentRunMetrics
	weightMap := map[string]int{}

	chaosExperiments, err := c.chaosExperimentOperator.GetExperiment(context.TODO(), bson.D{
		{"experiment_id", wfID},
	})
	if err != nil {
		return result, fmt.Errorf("failed to get experiment from db on complete, error: %w", err)
	}
	for _, rev := range chaosExperiments.Revision {
		if rev.RevisionID == execData.RevisionID {
			for _, weights := range rev.Weightages {
				weightMap[weights.FaultName] = weights.Weightage
				// Total weight calculated for all experiments
				weightSum = weightSum + weights.Weightage
			}
		}
	}

	result.TotalExperiments = len(weightMap)

	for _, value := range execData.Nodes {
		if value.Type == "ChaosEngine" {
			experimentName := ""
			if value.ChaosExp == nil {
				continue
			}

			for expName := range weightMap {
				if strings.Contains(value.ChaosExp.EngineName, expName) {
					experimentName = expName
				}
			}
			weight, ok := weightMap[experimentName]
			// probeSuccessPercentage will be included only if chaosData is present
			if ok {
				x, _ := strconv.Atoi(value.ChaosExp.ProbeSuccessPercentage)
				totalTestResult += weight * x
			}
			if value.ChaosExp.FaultVerdict == "Pass" {
				result.FaultsPassed += 1
			}
			if value.ChaosExp.FaultVerdict == "Fail" {
				result.FaultsFailed += 1
			}
			if value.ChaosExp.FaultVerdict == "Awaited" {
				result.FaultsAwaited += 1
			}
			if value.ChaosExp.FaultVerdict == "Stopped" {
				result.FaultsStopped += 1
			}
			if value.ChaosExp.FaultVerdict == "N/A" || value.ChaosExp.FaultVerdict == "" {
				result.FaultsNA += 1
			}
		}
	}
	if weightSum != 0 {
		result.ResiliencyScore = utils.Truncate(float64(totalTestResult) / float64(weightSum))
	}

	return result, nil
}
