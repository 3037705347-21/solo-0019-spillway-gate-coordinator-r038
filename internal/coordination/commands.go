package coordination

import (
	"context"
	"strings"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

func (s *Service) RequestCommand(
	ctx context.Context,
	request contracts.RequestCommandRequest,
) (domain.GateCommand, error) {
	gate, err := s.repository.Gate(ctx, request.GateID)
	if err != nil {
		return domain.GateCommand{}, err
	}

	commandID, err := s.newID("cmd")
	if err != nil {
		return domain.GateCommand{}, domain.Internal("command_id_generation_failed", "command id could not be generated")
	}

	command, err := domain.NewGateCommand(
		commandID,
		gate.ID,
		request.TargetAperture,
		gate.Aperture,
		gate.MaxAperture,
		request.Reason,
		request.RequestedBy,
		s.now(),
	)
	if err != nil {
		return domain.GateCommand{}, err
	}
	if err := s.repository.CreateCommand(ctx, command); err != nil {
		return domain.GateCommand{}, err
	}
	return command, nil
}

func (s *Service) Command(ctx context.Context, id string) (domain.GateCommand, error) {
	return s.repository.Command(ctx, id)
}

func (s *Service) ReplaceCommand(
	ctx context.Context,
	commandID string,
	request contracts.ReplaceCommandRequest,
) (domain.GateCommand, error) {
	expectedVersion := request.ExpectedVersion
	if request.Version != 0 {
		if expectedVersion != 0 && request.Version != expectedVersion {
			return domain.GateCommand{}, domain.Invalid("invalid_expected_version", "version fields must match")
		}
		expectedVersion = request.Version
	}
	if expectedVersion <= 0 {
		return domain.GateCommand{}, domain.Invalid("invalid_expected_version", "expected_version must be greater than zero")
	}

	if strings.TrimSpace(request.RequestedBy) == "" {
		existing, err := s.repository.Command(ctx, commandID)
		if err != nil {
			return domain.GateCommand{}, err
		}
		request.RequestedBy = existing.RequestedBy
	}
	return s.repository.ReplaceCommand(
		ctx,
		commandID,
		request.TargetAperture,
		request.Reason,
		request.RequestedBy,
		expectedVersion,
		s.now(),
	)
}

func (s *Service) ReviewInterlock(
	ctx context.Context,
	commandID string,
	request contracts.ReviewInterlockRequest,
) (domain.GateCommand, domain.Gate, error) {
	decision, err := domain.NewInterlockDecision(
		request.Verdict,
		request.Verifier,
		request.Note,
		s.now(),
	)
	if err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	token := ""
	if decision.IsClear() {
		token, err = s.newToken()
		if err != nil {
			return domain.GateCommand{}, domain.Gate{}, domain.Internal(
				"execution_token_generation_failed",
				"execution token could not be generated",
			)
		}
	}

	return s.repository.ReviewCommand(ctx, commandID, decision, token, s.now())
}

func (s *Service) ExecuteCommand(
	ctx context.Context,
	commandID string,
	request contracts.ExecuteCommandRequest,
) (domain.GateCommand, domain.Gate, error) {
	return s.repository.ExecuteCommand(
		ctx,
		commandID,
		request.Token,
		request.Operator,
		request.SimulateFault,
		s.now(),
	)
}
