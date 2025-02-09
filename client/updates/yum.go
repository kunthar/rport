package updates

import (
	"context"
	"fmt"
	"strings"

	chshare "github.com/cloudradar-monitoring/rport/share"
	"github.com/cloudradar-monitoring/rport/share/models"
)

type YumPackageManager struct {
	runner Runner
	cmd    string
}

func NewYumPackageManager() *YumPackageManager {
	return &YumPackageManager{
		runner: &RunnerImpl{},
		cmd:    "yum",
	}
}

func (p *YumPackageManager) IsAvailable(ctx context.Context) bool {
	// Can select either dnf or yum command, whichever is available
	for _, cmd := range []string{"dnf", "yum"} {
		p.cmd = cmd
		_, err := p.run(ctx, "help")
		if err == nil {
			return true
		}
	}
	return false
}

func (p *YumPackageManager) GetUpdatesStatus(ctx context.Context, logger *chshare.Logger) (*models.UpdatesStatus, error) {
	allUpdates, err := p.listUpdates(ctx, "--refresh")
	if err != nil {
		return nil, err
	}

	securityUpdates, err := p.listUpdates(ctx, "--security")
	if err != nil {
		return nil, err
	}
	isSecurity := make(map[string]bool, len(securityUpdates))
	for _, u := range securityUpdates {
		isSecurity[u.name] = true
	}

	summaries := make([]models.UpdateSummary, len(allUpdates))
	for i, update := range allUpdates {
		summaries[i] = models.UpdateSummary{
			Title:            update.name,
			Description:      fmt.Sprintf("%s %s [%s] (%s)", update.name, update.version, update.arch, update.repository),
			IsSecurityUpdate: isSecurity[update.name],
		}
	}

	rebootPending := p.checkRebootRequired(ctx)

	return &models.UpdatesStatus{
		UpdatesAvailable:         len(allUpdates),
		SecurityUpdatesAvailable: len(securityUpdates),
		UpdateSummaries:          summaries,
		RebootPending:            rebootPending,
	}, nil
}

type yumUpdate struct {
	name       string
	arch       string
	version    string
	repository string
}

func (p *YumPackageManager) listUpdates(ctx context.Context, additionalArgs ...string) ([]yumUpdate, error) {
	cmd := append([]string{"check-update", "--quiet"}, additionalArgs...)
	output, err := p.run(ctx, cmd...)
	// exit code 100 means updates available, so continue
	if err != nil && err.Error() != "exit status 100" {
		return nil, err
	}

	result := []yumUpdate{}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 {
			continue
		}

		update := yumUpdate{
			name:       parts[0],
			version:    parts[1],
			repository: parts[2],
		}

		nameArch := strings.Split(parts[0], ".")
		if len(nameArch) == 2 {
			update.name = nameArch[0]
			update.arch = nameArch[1]
		}

		result = append(result, update)
	}

	return result, nil
}

func (p *YumPackageManager) checkRebootRequired(ctx context.Context) bool {
	// The error is ignored here, since exit code is the same for reboot required and command does not exist.
	// So we cannot use it to determine if reboot is required.
	output, _ := p.run(ctx, "needs-restarting", "-r")
	return strings.Contains(output, "Reboot is required")
}

func (p *YumPackageManager) run(ctx context.Context, args ...string) (string, error) {
	fullCmd := append([]string{p.cmd}, args...)
	return p.runner.Run(ctx, fullCmd...)
}
