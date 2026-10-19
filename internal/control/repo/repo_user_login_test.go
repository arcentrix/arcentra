package repo

import (
	"github.com/arcentrix/arcentra/internal/control/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestUserLoginSecondUsernameOnlyAccount(t *testing.T) {
	db := userCreateDatabase(t)
	r := NewUserRepo(statusTestDB{db}, nil)
	require.NoError(t, db.Create(&model.User{UserID: "first-user", Username: "first", Email: "", Password: "first-stored-hash", IsEnabled: 1}).Error)
	require.NoError(t, db.Create(&model.User{UserID: "second-user", Username: "second", Email: "", Password: "second-stored-hash", IsEnabled: 1}).Error)
	got, err := r.Login(&model.Login{Username: "second"})
	require.NoError(t, err)
	require.Equal(t, "second-user", got.UserID, "blank email must not select an unrelated earlier account")
	require.Equal(t, "second-stored-hash", got.Password)
}

func TestUserLoginIdentifierSelection(t *testing.T) {
	db := userCreateDatabase(t)
	r := NewUserRepo(statusTestDB{db}, nil)
	require.NoError(t, db.Create(&[]model.User{
		{UserID: "empty-username", Username: "", Email: "unrelated@example.invalid", Password: "unrelated-hash", IsEnabled: 1},
		{UserID: "alpha-id", Username: "alpha", Email: "alpha@example.invalid", Password: "alpha-hash", IsEnabled: 1},
		{UserID: "beta-id", Username: "beta", Email: "beta@example.invalid", Password: "beta-hash", IsEnabled: 1},
		{UserID: "disabled-id", Username: "disabled", Email: "disabled@example.invalid", Password: "disabled-hash", IsEnabled: 0},
	}).Error)
	for _, tc := range []struct {
		name  string
		input *model.Login
		want  string
	}{
		{"username-only", &model.Login{Username: "beta"}, "beta-id"},
		{"email-only", &model.Login{Email: "beta@example.invalid"}, "beta-id"},
		{"both-match", &model.Login{Username: "beta", Email: "beta@example.invalid"}, "beta-id"},
		{"cross-account", &model.Login{Username: "alpha", Email: "beta@example.invalid"}, ""},
		{"username-match-email-missing", &model.Login{Username: "alpha", Email: "missing@example.invalid"}, ""},
		{"email-match-username-missing", &model.Login{Username: "missing", Email: "alpha@example.invalid"}, ""},
		{"neither", &model.Login{}, ""},
		{"nil", nil, ""},
		{"unknown-name", &model.Login{Username: "missing"}, ""},
		{"unknown-email", &model.Login{Email: "missing@example.invalid"}, ""},
		{"disabled-name", &model.Login{Username: "disabled"}, ""},
		{"disabled-email", &model.Login{Email: "disabled@example.invalid"}, ""},
		{"disabled-both", &model.Login{Username: "disabled", Email: "disabled@example.invalid"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Login(tc.input)
			if tc.want == "" {
				require.ErrorIs(t, err, gorm.ErrRecordNotFound)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got.UserID)
				require.Equal(t, 1, got.IsEnabled)
				require.Equal(t, "beta-hash", got.Password)
			}
		})
	}
}
