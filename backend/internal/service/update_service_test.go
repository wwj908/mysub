//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type updateServiceCacheStub struct {
	data string
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release     *GitHubRelease
	requestRepo string
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(_ context.Context, repo string) (*GitHubRelease, error) {
	s.requestRepo = repo
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	err := svc.PerformUpdate(context.Background(), "")

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func TestUpdateServiceChecksConfiguredGitHubRepository(t *testing.T) {
	client := &updateServiceGitHubClientStub{
		release: &GitHubRelease{
			TagName: "v0.1.138",
			Name:    "v0.1.138",
		},
	}
	svc := NewUpdateService(&updateServiceCacheStub{}, client, "0.1.137", "release")

	info, err := svc.CheckUpdate(context.Background(), true, "")

	require.NoError(t, err)
	require.Equal(t, "wwj908/mysub", client.requestRepo)
	require.True(t, info.HasUpdate)
}

func TestUpdateServiceChecksOfficialGitHubRepositoryWhenSelected(t *testing.T) {
	client := &updateServiceGitHubClientStub{
		release: &GitHubRelease{
			TagName: "v0.1.138",
			Name:    "v0.1.138",
		},
	}
	svc := NewUpdateService(&updateServiceCacheStub{}, client, "0.1.137", "release")

	info, err := svc.CheckUpdate(context.Background(), true, OfficialUpdateRepo)

	require.NoError(t, err)
	require.Equal(t, OfficialUpdateRepo, client.requestRepo)
	require.Equal(t, OfficialUpdateRepo, info.UpdateRepo)
	require.True(t, info.HasUpdate)
}
