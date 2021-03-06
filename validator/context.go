package validator

import (
	"context"
	"log"
	"reflect"
	"time"

	"github.com/google/go-github/github"
	"github.com/pkg/errors"
)

// Context contains an event payload an a configured client
type Context struct {
	Event     interface{}
	Github    *github.Client
	Ctx       *context.Context
	AppID     *int
	AppGitHub *github.Client
}

// Process handles webhook events kinda like Probot does
func (c *Context) Process() bool {
	switch e := c.Event.(type) {
	case *github.CheckSuiteEvent:
		c.ProcessCheckSuite(c.Event.(*github.CheckSuiteEvent))
		return true
	case *github.PullRequestEvent:
		return c.ProcessPrEvent(c.Event.(*github.PullRequestEvent))
	case *github.CheckRunEvent:
		return c.ProcessCheckRunEvent(c.Event.(*github.CheckRunEvent))
	case *github.InstallationEvent:
		err := c.LogInstallationCount()
		if err != nil {
			log.Printf("%+v\n", err)
			return false
		}
		return true
	case *github.InstallationRepositoriesEvent:
		err := c.LogInstallationCount()
		if err != nil {
			log.Printf("%+v\n", err)
			return false
		}
		return true
	default:
		log.Printf("ignoring %s\n", reflect.TypeOf(e).String())
	}
	return false
}

// ProcessCheckSuite validates the Kubernetes YAML that has changed on checks
// associated with PRs.
func (c *Context) ProcessCheckSuite(e *github.CheckSuiteEvent) {
	if *e.Action == "created" || *e.Action == "requested" || *e.Action == "rerequested" {
		createCheckRunErr := c.createInitialCheckRun(e)
		if createCheckRunErr != nil {
			// TODO return a 500 to signal that retry is preferred
			log.Println(errors.Wrap(createCheckRunErr, "Couldn't create check run"))
			return
		}

		checkRunStart := time.Now()
		var annotations []*github.CheckRunAnnotation
		var candidates Candidates

		config, configAnnotation, err := c.kubeValidatorConfigOrAnnotation(e)
		if err != nil {
			c.createConfigMissingCheckRun(&checkRunStart, e)
			return
		}
		if configAnnotation != nil {
			annotations = append(annotations, configAnnotation)
			c.createConfigInvalidCheckRun(&checkRunStart, e, annotations)
			return
		}

		// Determine which files to validate
		changedFileList, fileListError := c.changedFileList(e)
		if fileListError != nil {
			// TODO fail the checkrun instead
			log.Println(fileListError)
			return
		}

		candidates = config.matchingCandidates(c, changedFileList)
		annotations = append(annotations, candidates.LoadBytes()...)
		annotations = append(annotations, candidates.Validate()...)

		// Annotate the PR
		finalCheckRunErr := c.createFinalCheckRun(&checkRunStart, e, candidates, annotations)
		if finalCheckRunErr != nil {
			// TODO return a 500 to signal that retry is preferred
			log.Println(errors.Wrap(finalCheckRunErr, "Couldn't create check run"))
			return
		}
	}
	return
}

// ProcessPrEvent re-requests check suites on PRs when they're opened or re-opened
func (c *Context) ProcessPrEvent(e *github.PullRequestEvent) bool {
	if *e.Action == "opened" || *e.Action == "reopened" {

		results, _, err := c.Github.Checks.ListCheckSuitesForRef(*c.Ctx, e.Repo.GetOwner().GetLogin(), e.Repo.GetName(), e.PullRequest.Head.GetRef(), &github.ListCheckSuiteOptions{
			AppID: c.AppID,
		})
		if err != nil {
			log.Printf("%+v\n", err)
		}
		if results.GetTotal() == 1 {
			suite := results.CheckSuites[0]
			_, err := c.Github.Checks.ReRequestCheckSuite(*c.Ctx, e.Repo.GetOwner().GetLogin(), e.Repo.GetName(), suite.GetID())
			if err != nil {
				log.Printf("%+v\n", err)
			}
			return true
		}
	}
	return false
}

// ProcessCheckRunEvent re-requests CheckSuites when a conatined CheckRun is rerequested
func (c *Context) ProcessCheckRunEvent(e *github.CheckRunEvent) bool {
	if *e.Action == "rerequested" {

		_, err := c.Github.Checks.ReRequestCheckSuite(*c.Ctx, e.Repo.GetOwner().GetLogin(), e.Repo.GetName(), e.CheckRun.CheckSuite.GetID())
		if err != nil {
			log.Printf("%+v\n", err)
			return false
		}
		return true
	}
	return false
}

// LogInstallationCount logs the number of installations to help keep track of
// eligibility for inclusion in the GitHub Marketplace.
// https://developer.github.com/apps/marketplace/creating-and-submitting-your-app-for-approval/requirements-for-listing-an-app-on-github-marketplace/
func (c *Context) LogInstallationCount() error {
	installations, _, err := c.AppGitHub.Apps.ListInstallations(*c.Ctx, &github.ListOptions{
		PerPage: 251,
	})
	if err != nil {
		return err
	}
	installationCount := len(installations)
	if installationCount > 250 {
		log.Printf("%+v installations. get thee to the market!", installationCount)
	} else {
		log.Printf("%+v installations. keep it up!", installationCount)
	}
	return nil
}
