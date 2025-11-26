package git_tools

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v48/github"
)

type NotificationTODO struct {
}

func (n NotificationTODO) ItemTitle(indent_level int) string {
	return "abc"
}

func GetNotifications() []*github.Notification {
	client := GetGithubClient()
	options := github.NotificationListOptions{}
	notifications, _, err := client.Activity.ListNotifications(context.Background(), &options)
	if err != nil {
		fmt.Println("Error gathering nofications: {)", err)
	}
	return notifications
}

func MarkRead(since time.Time) {
	client := GetGithubClient()
	client.Activity.MarkRepositoryNotificationsRead(context.Background(), "owner", "repo", since)
}
