package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	log "github.com/sirupsen/logrus"
)

type CloudTrailFile struct {
	Records []map[string]interface{} `json:"Records"`
}

func init() {
}

func main() {
	log.SetFormatter(&log.JSONFormatter{})
	log.Info("Starting v0.1.5")
	lambda.Start(S3Handler)
}

func S3Handler(ctx context.Context, s3Event events.S3Event) error {
	log.Infof("S3 event: %v", s3Event)

	for _, s3Record := range s3Event.Records {
		err := Stream(s3Record)
		if err != nil {
			return err
		}
	}

	return nil
}

func FilterRecords(logFile *CloudTrailFile, evt events.S3EventRecord) error {
	for _, record := range logFile.Records {
		userIdentity, _ := record["userIdentity"].(map[string]interface{})

		if userIdentity["invokedBy"] == "AWS Internal" {
			continue
		}

		switch en := record["eventName"].(string); {
		// Some events don't match AWS defined standards
		// So we have to convert the input to Title
		case strings.HasPrefix(strings.Title(en), "Get"):
			continue
		// Some events don't match AWS defined standards
		// So we have to convert the input to Title
		case strings.HasPrefix(strings.Title(en), "List"):
			continue
		// Some events don't match AWS defined standards
		// So we have to convert the input to Title
		case strings.HasPrefix(strings.Title(en), "View"):
			continue
		case strings.HasPrefix(en, "Head"):
			continue
		case strings.HasPrefix(en, "Describe"):
			continue
		case strings.HasPrefix(en, "Test"):
			continue
		case strings.HasPrefix(en, "Download"):
			continue
		case strings.HasPrefix(en, "Report"):
			continue
		case strings.HasPrefix(en, "Poll"):
			continue
		case strings.HasPrefix(en, "Verify"):
			continue
		case strings.HasPrefix(en, "Skip"):
			continue
		case strings.HasPrefix(en, "Count"):
			continue
		case strings.HasPrefix(en, "Detect"):
			continue
		case strings.HasPrefix(en, "Lookup"):
			continue
		case en == "ConsoleLogin":
			continue
		case strings.HasSuffix(en, "VirtualMFADevice"):
			continue
		case en == "CheckMfa":
			continue
		case en == "CheckDomainAvailability":
			continue
		case en == "Decrypt":
			continue
		case en == "SetTaskStatus":
			continue
		case en == "BatchGetQueryExecution":
			continue
		case en == "QueryObjects":
			continue
		case strings.HasPrefix(en, "StartQuery"):
			continue
		case strings.HasPrefix(en, "StopQuery"):
			continue
		case strings.HasPrefix(en, "CancelQuery"):
			continue
		case strings.HasPrefix(en, "BatchGet"):
			continue
		case strings.HasPrefix(en, "Search"):
			continue
		case en == "GenerateServiceLastAccessedDetails":
			continue
		case en == "REST.GET.OBJECT_LOCK_CONFIGURATION":
			continue
		case en == "AssumeRoleWithWebIdentity":
			continue
		case en == "PutQueryDefinition":
			if record["eventSource"] == "logs.amazonaws.com" {
				continue
			}
		case en == "PutObject":
			// Fingerprinting on KeyPath for LB Logs
			// Objects are originating outside our account with these account ids.
			// https://docs.aws.amazon.com/elasticloadbalancing/latest/classic/enable-access-logs.html
			if rps, ok := record["requestParameters"].(map[string]interface{}); ok {
				if k, ok := rps["key"].(string); ok {
					if strings.HasPrefix(k, "elb/AWSLogs") {
						continue
					}
				}
			}

		case en == "AssumeRole":
			if record["userAgent"] == "Coral/Netty4" {
				switch userIdentity["invokedBy"] {
				case
					"ecs-tasks.amazonaws.com",
					"ec2.amazonaws.com",
					"monitoring.rds.amazonaws.com",
					"lambda.amazonaws.com":
					continue
				}
			}
		}

		if usa, ok := record["userAgent"]; ok {
			switch ua := usa.(string); {
			case ua == "console.amazonaws.com":
				break
			case ua == "signin.amazonaws.com":
				break
			case ua == "Coral/Jakarta":
				break
			case ua == "Coral/Netty4":
				break
			case ua == "AWS CloudWatch Console":
				break
			case strings.HasPrefix(ua, "AWS Signin"):
				break
			case strings.HasPrefix(ua, "S3Console/"):
				break
			case strings.HasPrefix(ua, "[S3Console"):
				break
			case strings.HasPrefix(ua, "Mozilla/"):
				break
			case matchString("console.*.amazonaws.com", ua):
				break
			case matchString("signin.*.amazonaws.com", ua):
				break
			case matchString("aws-internal*", ua):
				break
			default:
				continue
			}
		}

		userName := fmt.Sprintf("%s", userIdentity["principalId"])
		if strings.Contains(userName, ":") {
			userName = strings.Split(userName, ":")[1]
		}
		if userIdentity["userName"] != nil {
			userName = fmt.Sprintf("%s", userIdentity["userName"])
		}

		log.WithFields(log.Fields{
			"user_agent":   record["userAgent"],
			"event_time":   record["eventTime"],
			"principal":    userIdentity["principalId"],
			"user_name":    userName,
			"event_source": record["eventSource"],
			"event_name":   record["eventName"],
			"account_id":   userIdentity["accountId"],
			"event_id":     record["eventID"],
			"s3_uri":       fmt.Sprintf("s3://%s/%s", evt.S3.Bucket.Name, evt.S3.Object.Key),
		}).Info("Event")

		if webhookUrl, ok := os.LookupEnv("SLACK_WEBHOOK"); ok {
			slackBody := fmt.Sprintf(`
{
  "channel": "%s",
  "text": "Not Used",
  "blocks": [
    {
      "type": "section",
      "text": {
        "type": "mrkdwn",
        "text": "*%s* - %s"
      }
    },
    {
      "type": "context",
      "elements": [
        {
          "type": "mrkdwn",
          "text": "%s"
        },
        {
          "type": "mrkdwn",
          "text": "%s"
        },
        {
          "type": "mrkdwn",
          "text": "<https://console.aws.amazon.com/cloudtrail/home?region=%s#/events?EventId=%s|%s>"
        }
      ]
    }
  ]
}
`,
				os.Getenv("SLACK_CHANNEL"),
				record["eventName"],
				record["eventSource"],
				getEnv(
					fmt.Sprintf("SLACK_NAME_%s", userIdentity["accountId"]),
					getEnv("SLACK_NAME", fmt.Sprintf("%s", userIdentity["accountId"]))),
				userName,
				record["awsRegion"],
				record["eventID"],
				record["eventTime"])

			err := SendSlackNotification(webhookUrl, []byte(slackBody))
			if err != nil {
				log.Debugln(slackBody)
				log.Debug(err)
			}
		}
	}
	// log.Infof("Scanned %d records", len(logFile.Records))
	return nil
}

func Stream(evt events.S3EventRecord) error {
	s3ClientConfig := aws.NewConfig().WithRegion(evt.AWSRegion)
	s3Client := s3.New(session.Must(session.NewSession()), s3ClientConfig)
	s3Bucket := evt.S3.Bucket.Name
	s3Object := evt.S3.Object.Key

	log.Debugf("Reading %s from %s with client config of %+v", s3Object, s3Bucket, s3Client.Config)

	obj, err := fetchLogFromS3(s3Client, s3Bucket, s3Object)
	if err != nil {
		return fmt.Errorf("%v: %v", s3Object, err)
	}
	if obj == nil {
		return nil
	}

	logFile, err := readLogFile(obj)
	if err != nil {
		return fmt.Errorf("%v: %v", s3Object, err)
	}

	err = FilterRecords(logFile, evt)
	if err != nil {
		return fmt.Errorf("%v: %v", s3Object, err)
	}

	return nil
}

func fetchLogFromS3(s3Client *s3.S3, s3Bucket string, s3Object string) (*s3.GetObjectOutput, error) {
	logInput := &s3.GetObjectInput{
		Bucket: aws.String(s3Bucket),
		Key:    aws.String(s3Object),
	}

	if strings.Contains(s3Object, "/CloudTrail-Digest/") || strings.Contains(s3Object, "/Config/") {
		return nil, nil
	}

	obj, err := s3Client.GetObject(logInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			return nil, fmt.Errorf("AWS Error: %v", aerr)
		}
		return nil, fmt.Errorf("Error getting S3 Object: %v", err)
	}

	return obj, nil
}

func readLogFile(object *s3.GetObjectOutput) (*CloudTrailFile, error) {
	defer object.Body.Close()

	var logFileBlob io.ReadCloser
	var err error
	if object.ContentType != nil && *object.ContentType == "application/x-gzip" {
		logFileBlob, err = gzip.NewReader(object.Body)
		if err != nil {
			return nil, fmt.Errorf("extracting json.gz file: %v", err)
		}
		defer logFileBlob.Close()
	} else {
		logFileBlob = object.Body
	}

	blobBuf := new(bytes.Buffer)
	_, err = blobBuf.ReadFrom(logFileBlob)
	if err != nil {
		return nil, fmt.Errorf("Error reading from logFileBlob: %v", err)
	}

	var logFile CloudTrailFile
	err = json.Unmarshal(blobBuf.Bytes(), &logFile)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling s3 object to CloudTrailFile: %v", err)
	}

	return &logFile, nil
}

func matchString(m, s string) bool {
	v, _ := regexp.MatchString(m, s)
	return v
}

func prettyPrint(i interface{}) string {
	s, _ := json.MarshalIndent(i, "", "  ")
	return string(s)
}

func SendSlackNotification(webhookUrl string, slackBody []byte) error {

	req, err := http.NewRequest(http.MethodPost, webhookUrl, bytes.NewBuffer(slackBody))
	if err != nil {
		return err
	}

	req.Header.Add("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if buf.String() != "ok" {
		return errors.New(fmt.Sprintf("Non-ok response returned from Slack: %s", buf.String()))
	}
	return nil
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		if value == "" {
			return fallback
		}
		return value
	}
	return fallback
}
