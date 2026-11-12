package cli

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// Delivery identifies pre-existing command/replay resources. Preparation never opens SDK clients.
type Delivery struct {
	TasksQueue     string `json:"tasksQueue,omitempty"`
	URL            string `json:"url,omitempty"`
	ServiceAccount string `json:"serviceAccount,omitempty"`
	Audience       string `json:"audience,omitempty"`
	QueueURL       string `json:"queueURL,omitempty"`
	GroupID        string `json:"groupID,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
}

func (c Config) validateDelivery() error {
	d := c.Delivery
	if d == nil {
		return nil
	}
	if c.Provider == "gcp" {
		parts := strings.Split(d.TasksQueue, "/")
		u, err := url.Parse(d.URL)
		if len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "locations" || parts[3] == "" || parts[4] != "queues" || parts[5] == "" {
			return errors.New("delivery tasksQueue must name projects/.../locations/.../queues/...")
		}
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path == "" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("delivery URL must be an exact HTTPS endpoint")
		}
		if strings.TrimSpace(d.Audience) == "" || strings.TrimSpace(d.ServiceAccount) != d.ServiceAccount || !strings.HasSuffix(d.ServiceAccount, ".gserviceaccount.com") || strings.Count(d.ServiceAccount, "@") != 1 || strings.HasPrefix(d.ServiceAccount, "@") {
			return errors.New("delivery requires audience and exact Google service account")
		}
		if d.QueueURL != "" || d.GroupID != "" || d.Timeout != "" {
			return errors.New("GCP delivery forbids AWS queue settings")
		}
	} else if c.Provider == "aws" {
		u, err := url.Parse(d.QueueURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path == "" || u.Path == "/" || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("delivery queueURL must name an HTTPS SQS queue")
		}
		if c.Publication != nil && d.QueueURL == c.Publication.QueueURL {
			return errors.New("delivery and publication queues must be distinct")
		}
		fifo := strings.HasSuffix(d.QueueURL, ".fifo")
		if (fifo && (strings.TrimSpace(d.GroupID) == "" || len(d.GroupID) > 128)) || (!fifo && d.GroupID != "") {
			return errors.New("delivery groupID required only for FIFO, max128 bytes")
		}
		if d.Timeout != "" {
			v, err := time.ParseDuration(d.Timeout)
			if err != nil || v < time.Second || v > 11*time.Hour {
				return errors.New("delivery timeout must be1s..11h")
			}
		}
		if d.TasksQueue != "" || d.URL != "" || d.ServiceAccount != "" || d.Audience != "" {
			return errors.New("AWS delivery forbids GCP task settings")
		}
	}
	return nil
}
func (c Config) deliveryEnv(env map[string]string) {
	if c.Delivery == nil {
		return
	}
	d := c.Delivery
	for key, value := range map[string]string{"DELIVERY_TASKS_QUEUE": d.TasksQueue, "DELIVERY_URL": d.URL, "DELIVERY_SERVICE_ACCOUNT": d.ServiceAccount, "DELIVERY_AUDIENCE": d.Audience, "DELIVERY_QUEUE_URL": d.QueueURL, "DELIVERY_GROUP_ID": d.GroupID, "DELIVERY_TIMEOUT": d.Timeout} {
		if value != "" {
			env[key] = value
		}
	}
}
