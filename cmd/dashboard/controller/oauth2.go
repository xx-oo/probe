package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code.gitea.io/sdk/gitea"
	"github.com/gin-gonic/gin"
	GitHubAPI "github.com/google/go-github/v47/github"
	"github.com/patrickmn/go-cache"
	"github.com/xanzy/go-gitlab"
	"golang.org/x/oauth2"
	GitHubOauth2 "golang.org/x/oauth2/github"
	GitlabOauth2 "golang.org/x/oauth2/gitlab"

	"github.com/naiba/nezha/model"
	"github.com/naiba/nezha/pkg/mygin"
	"github.com/naiba/nezha/pkg/utils"
	"github.com/naiba/nezha/service/singleton"
)

type oauth2controller struct {
	r gin.IRoutes
}

func (oa *oauth2controller) serve() {
	oa.r.GET("/oauth2/login", oa.login)
	oa.r.GET("/oauth2/callback", oa.callback)
}

func (oa *oauth2controller) getCommonOauth2Config(c *gin.Context) *oauth2.Config {
	if singleton.Conf.Oauth2.Type == model.ConfigTypeGitee {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://gitee.com/oauth/authorize",
				TokenURL: "https://gitee.com/oauth/token",
			},
			RedirectURL: oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitlab {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{"read_user", "read_api"},
			Endpoint:     GitlabOauth2.Endpoint,
			RedirectURL:  oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeJihulab {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{"read_user", "read_api"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://jihulab.com/oauth/authorize",
				TokenURL: "https://jihulab.com/oauth/token",
			},
			RedirectURL: oa.getRedirectURL(c),
		}
	} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitea {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  fmt.Sprintf("%s/login/oauth/authorize", singleton.Conf.Oauth2.Endpoint),
				TokenURL: fmt.Sprintf("%s/login/oauth/access_token", singleton.Conf.Oauth2.Endpoint),
			},
			RedirectURL: oa.getRedirectURL(c),
		}
	} else {
		return &oauth2.Config{
			ClientID:     singleton.Conf.Oauth2.ClientID,
			ClientSecret: singleton.Conf.Oauth2.ClientSecret,
			Scopes:       []string{},
			Endpoint:     GitHubOauth2.Endpoint,
		}
	}
}

func (oa *oauth2controller) getRedirectURL(c *gin.Context) string {
	scheme := "http://"
	if strings.HasPrefix(c.Request.Referer(), "https://") {
		scheme = "https://"
	}
	return scheme + c.Request.Host + "/oauth2/callback"
}

func (oa *oauth2controller) login(c *gin.Context) {
	randomString, err := utils.GenerateRandomString(32)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "Something Wrong",
			Msg:   err.Error(),
		}, true)
		return
	}
	state, stateKey := randomString[:16], randomString[16:]
	singleton.Cache.Set(fmt.Sprintf("%s%s", model.CacheKeyOauth2State, stateKey), state, cache.DefaultExpiration)
	url := oa.getCommonOauth2Config(c).AuthCodeURL(state, oauth2.AccessTypeOnline)
	c.SetCookie(singleton.Conf.Site.CookieName+"-sk", stateKey, 60*5, "", "", false, false)
	c.HTML(http.StatusOK, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/redirect", mygin.CommonEnvironment(c, gin.H{
		"URL": url,
	}))
}

func (oa *oauth2controller) callback(c *gin.Context) {
	var err error
	// 验证登录跳转时的 State
	stateKey, err := c.Cookie(singleton.Conf.Site.CookieName + "-sk")
	if err == nil {
		state, ok := singleton.Cache.Get(fmt.Sprintf("%s%s", model.CacheKeyOauth2State, stateKey))
		if !ok || state.(string) != c.Query("state") {
			err = errors.New("非法的登录方式")
		}
	}
	oauth2Config := oa.getCommonOauth2Config(c)
	ctx := context.Background()
	var otk *oauth2.Token
	if err == nil {
		otk, err = oauth2Config.Exchange(ctx, c.Query("code"))
	}

	var user model.User

	if err == nil {
		if singleton.Conf.Oauth2.Type == model.ConfigTypeGitlab || singleton.Conf.Oauth2.Type == model.ConfigTypeJihulab {
			var gitlabApiClient *gitlab.Client
			if singleton.Conf.Oauth2.Type == model.ConfigTypeGitlab {
				gitlabApiClient, err = gitlab.NewOAuthClient(otk.AccessToken)
			} else {
				gitlabApiClient, err = gitlab.NewOAuthClient(otk.AccessToken, gitlab.WithBaseURL("https://jihulab.com/api/v4/"))
			}
			var u *gitlab.User
			if err == nil {
				u, _, err = gitlabApiClient.Users.CurrentUser()
			}
			if err == nil {
				user = model.NewUserFromGitlab(u)
			}
		} else if singleton.Conf.Oauth2.Type == model.ConfigTypeGitea {
			var giteaApiClient *gitea.Client
			giteaApiClient, err = gitea.NewClient(singleton.Conf.Oauth2.Endpoint, gitea.SetToken(otk.AccessToken))
			var u *gitea.User
			if err == nil {
				u, _, err = giteaApiClient.GetMyUserInfo()
			}
			if err == nil {
				user = model.NewUserFromGitea(u)
			}
		} else {
			var client *GitHubAPI.Client
			oc := oauth2Config.Client(ctx, otk)
			if singleton.Conf.Oauth2.Type == model.ConfigTypeGitee {
				baseURL, _ := url.Parse("https://gitee.com/api/v5/")
				uploadURL, _ := url.Parse("https://gitee.com/api/v5/uploads/")
				client = GitHubAPI.NewClient(oc)
				client.BaseURL = baseURL
				client.UploadURL = uploadURL
			} else {
				client = GitHubAPI.NewClient(oc)
			}
			var gu *GitHubAPI.User
			if err == nil {
				gu, _, err = client.Users.Get(ctx, "")
			}
			if err == nil {
				user = model.NewUserFromGitHub(gu)
			}
		}
	}

	if err == nil && user.Login == "" {
		err = errors.New("获取用户信息失败")
	}

	if err != nil || user.Login == "" {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "登录失败",
			Msg:   fmt.Sprintf("错误信息：%s", err),
		}, true)
		return
	}
	var isAdmin bool
	for _, admin := range strings.Split(singleton.Conf.Oauth2.Admin, ",") {
		if admin != "" && strings.EqualFold(user.Login, admin) {
			isAdmin = true
			break
		}
	}
	if !isAdmin {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "登录失败",
			Msg:   fmt.Sprintf("错误信息：%s", "该用户不是本站点管理员，无法登录"),
		}, true)
		return
	}
	user.Token, err = utils.GenerateRandomString(32)
	if err != nil {
		mygin.ShowErrorPage(c, mygin.ErrInfo{
			Code:  http.StatusBadRequest,
			Title: "Something wrong",
			Msg:   err.Error(),
		}, true)
		return
	}
	user.TokenExpired = time.Now().AddDate(0, 2, 0)
	singleton.DB.Save(&user)
	c.SetCookie(singleton.Conf.Site.CookieName, user.Token, 60*60*24, "", "", false, false)
	c.HTML(http.StatusOK, "dashboard-"+singleton.Conf.Site.DashboardTheme+"/redirect", mygin.CommonEnvironment(c, gin.H{
		"URL": "/",
	}))
}
