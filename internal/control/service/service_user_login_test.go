package service

import (
	"encoding/base64"
	"errors"
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/arcentrix/arcentra/internal/control/repo"
	"github.com/arcentrix/arcentra/pkg/http"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"testing"
)

type loginFailureRepository struct {
	repo.IUserRepository
	user *model.User
	err  error
}

func (r loginFailureRepository) Login(*model.Login) (*model.User, error) { return r.user, r.err }
func TestUserLoginPublicCredentialFailuresAreIndistinguishable(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-correct-password"), bcrypt.MinCost)
	require.NoError(t, err)
	validUser := &model.User{UserID: "user", Username: "user", Email: "user@example.invalid", Password: string(hash), IsEnabled: 1}
	wrong := base64.StdEncoding.EncodeToString([]byte("synthetic-wrong-password"))
	for _, tc := range []struct {
		name  string
		input *model.Login
		user  *model.User
		err   error
	}{
		{"unknown-or-disabled", &model.Login{Username: "user", Password: wrong}, nil, gorm.ErrRecordNotFound},
		{"database-error", &model.Login{Username: "user", Password: wrong}, nil, errors.New("SQL private-table secret-driver-detail")},
		{"missing-row", &model.Login{Username: "user", Password: wrong}, nil, nil},
		{"username-mismatch", &model.Login{Username: "other", Password: wrong}, validUser, nil},
		{"malformed-password", &model.Login{Username: "user", Password: "not-base64!"}, validUser, nil},
		{"wrong-password", &model.Login{Username: "user", Password: wrong}, validUser, nil},
		{"no-identifier", &model.Login{}, nil, nil},
		{"nil-request", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &UserService{userRepo: loginFailureRepository{user: tc.user, err: tc.err}}
			got, err := svc.Login(tc.input, http.Auth{})
			require.Nil(t, got)
			require.EqualError(t, err, "Invalid credentials")
		})
	}
}
func TestUserLoginRepositorySelectionKeepsStoredBcrypt(t *testing.T) {
	svc, db := registrationServiceFixture(t, nil)
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-correct-password"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, db.Create(&[]model.User{{UserID: "first", Username: "first", IsEnabled: 1}, {UserID: "second", Username: "second", Password: string(hash), IsEnabled: 1}}).Error)
	selected, err := svc.userRepo.Login(&model.Login{Username: "second"})
	require.NoError(t, err)
	require.Equal(t, "second", selected.UserID)
	require.True(t, comparePassword(selected.Password, "synthetic-correct-password"))
	require.False(t, comparePassword(selected.Password, "synthetic-wrong-password"))
}
