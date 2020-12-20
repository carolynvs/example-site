// +build mage

// This is a magefile, and is a "makefile for go".
// See https://magefile.org/
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/carolynvs/magex/pkg"
	"github.com/carolynvs/magex/shx"
	"github.com/carolynvs/magex/xplat"
	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/pkg/errors"
)

var (
	// Default target to run when none is specified
	// If not set, running mage will list available targets
	Default = Preview

	// By default we look for a local clone of the contentRepo at
	// ../REPO_NAME, e.g. ../example-site-content. Override with
	// the environment variable CONTENT_REPOS if necessary.
	localContentEnvVar = "CONTENT_REPOS"

	// The repository that contains your content
	contentRepos = []string{
		"github.com/carolynvs/example-site-content",
		"github.com/carolynvs/example-site-docs",
	}
)

const (
	containerName = "example-site"
	img           = containerName
)

// Ensure Mage is installed and on the PATH.
func EnsureMage() error {
	return pkg.EnsureMage("")
}

// Compile the website to website/public.
func Build() error {
	mg.Deps(clean, buildImage)

	// Build the volume mount for a local contribute repo, if present
	contentMounts, goModMount, err := useLocalContent()
	if err != nil {
		return err
	}

	pwd, _ := os.Getwd()
	return sh.RunV("docker", expandArgs("run", "--rm", "-v", pwd+":/src",
		contentMounts, goModMount, containerName, "--debug", "--verbose")...)
}

// Run a local server to preview the website and watch for changes.
func Preview() error {
	mg.Deps(clean, buildImage)

	// Build the volume mount for a local content repo, if present
	contentMounts, goModMount, err := useLocalContent()
	if err != nil {
		return err
	}

	port := getPort()
	pwd, _ := os.Getwd()
	err = sh.RunV("docker", expandArgs("run", "-d", "-v", pwd+":/src",
		contentMounts, goModMount, "-p", port+":1313",
		"--name", containerName, img, "server", "--debug", "--verbose",
		"--buildDrafts", "--buildFuture", "--noHTTPCache", "--watch", "--bind=0.0.0.0")...)
	if err != nil {
		return errors.Wrap(err, "could not run website container")
	}

	err = awaitContainer(containerName, "Web Server is available")
	if err != nil {
		return errors.Wrap(err, "error waiting for the website to become ready")
	}

	url := "http://localhost:" + getPort()
	return errors.Wrap(openURL(url), "could not open the website in a browser")
}

// Use hugo in a docker container.
func Hugo() error {
	mg.Deps(clean, buildImage)

	// Build the volume mount for a local content repo, if present
	contentMounts, goModMount, err := useLocalContent()
	if err != nil {
		return err
	}

	pwd, _ := os.Getwd()
	args := []string{}
	for _, m := range contentMounts {
		args = append(args, m)
	}
	cmd := sh.Command("docker", expandArgs("run", "--rm", "-it", "-v", pwd+":/src",
		contentMounts, goModMount, img, "shell")...).
		Stdout(os.Stdout)
	cmd.Cmd.Stdin = os.Stdin
	_, _, err = cmd.Run()
	return errors.Wrap(err, "could not start hugo in a container")
}

// expandArgs takes any arrays and makes them separate arguments,
// and then removes empty arguments.
func expandArgs(args ...interface{}) []string {
	expandedArgs := make([]string, len(args))
	for _, arg := range args {
		if combinedArg, ok := arg.([]string); ok {
			for _, a := range combinedArg {
				expandedArgs = append(expandedArgs, a)
			}
		} else if stringArg, ok := arg.(string); ok {
			expandedArgs = append(expandedArgs, stringArg)
		}
	}
	return shx.CollapseArgs(expandedArgs...)
}

// Create go.local.mod with any appropriate replace statements, and
// returns the local content mount flags if present.
func useLocalContent() (contentMounts []string, goModMount string, err error) {
	// package name=local path,package name=local path
	// e.g. github.com/carolynvs/example-site-content=../content
	localContent := map[string]string{}
	contentOverrides := os.Getenv(localContentEnvVar)
	for _, override := range strings.Split(contentOverrides, ",") {
		parts := strings.Split(override, "=")
		if len(parts) != 2 {
			return nil, "", errors.Errorf(
				`overrides in CONTENT_REPOS is incorrectly formatted, expected "package name=local path,package name=local path"
For example, "github.com/carolynvs/example-site-content=../content,github.com/carolynvs/example-site-docs=../docs"
Got: %q`, contentOverrides)
		}

		contentRepo := parts[0]
		path := parts[1]
		localContent[contentRepo] = path
	}

	contentMounts = make([]string, 0, len(contentRepos))
	for _, contentRepo := range contentRepos {
		contentMount, err := buildContentMount(contentRepo, localContent[contentRepo])
		if err != nil {
			return nil, "", err
		}

		if contentMount != "" {
			contentMounts = append(contentMounts, contentMount)
		}
	}

	if len(contentMounts) == 0 {
		return nil, "", nil
	}

	pwd, _ := os.Getwd()
	localGoMod := filepath.Join(pwd, "go.local.mod")
	err = copyFile("go.mod", localGoMod)
	if err != nil {
		return nil, "", err
	}
	goModMount = fmt.Sprintf("-v=%s:/src/go.mod", localGoMod)

	args := []string{"run", "--rm", "--entrypoint", "go", "-v", pwd + ":/src",
		goModMount, img, "mod", "edit"}
	for contentRepo := range localContent {
		contentDirName := path.Base(contentRepo)
		args = append(args, "-replace", contentRepo+"=/src/"+contentDirName)
	}
	err = sh.RunV("docker", args...)
	if err != nil {
		return nil, "", errors.Wrap(err, "could not modify go.mod to use your local content repositories")
	}

	return contentMounts, goModMount, nil
}

func buildContentMount(contentRepo string, localContent string) (contentMount string, err error) {
	contentDirName := path.Base(contentRepo)
	if localContent == "" {
		localContent = filepath.Join("..", contentDirName)
	}
	localContentPath, _ := filepath.Abs(localContent)

	if mg.Verbose() {
		fmt.Printf("Checking for a local copy of %s at %s\n", contentRepo, localContentPath)
	}

	// Only mount the local repo if it exists, otherwise use the one on github
	_, err = os.Stat(localContentPath)
	if err != nil {
		return "", nil
	}

	log.Printf("Using your local copy of %s -> %s\n", contentRepo, localContentPath)
	contentMount = fmt.Sprintf("-v=%s:/src/"+contentDirName, localContentPath)
	return contentMount, nil
}

func copyFile(src string, dest string) error {
	s, err := os.Open(src)
	if err != nil {
		return errors.Wrapf(err, "could not open %s", src)
	}

	d, err := os.Create(dest)
	if err != nil {
		return errors.Wrapf(err, "could not create %s", dest)
	}

	_, err = io.Copy(d, s)
	return errors.Wrapf(err, "could not copy %s to %s", src, dest)
}

func openURL(url string) error {
	shell := xplat.DetectShell()
	if shell == "msystem" {
		return shx.RunE("cmd", "/C", "open "+url)
	}
	if runtime.GOOS == "windows" {
		return shx.RunE("powershell", "Start-Process", url)
	}
	return shx.RunE(shell, "-c", "open "+url)
}

func getPort() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "1313"
	}
	return port
}

func buildImage() error {
	mg.Deps(docsy)
	err := shx.RunE("docker", "build", "-t", img,
		"-f", "dev.Dockerfile", ".")
	return errors.Wrap(err, "could not build website image")
}

func docsy() error {
	_, err := os.Stat("themes/docsy")
	if err != nil {
		if os.IsNotExist(err) {
			return shx.RunE("git", "submodule", "update", "--init", "--recursive", "--force")
		}
		return errors.Wrap(err, "could not clone the docsy theme")
	}

	return nil
}

func containerExists(name string) bool {
	output, err := sh.Output("docker", "ps", "--all", "--filter", "name="+name)
	return err == nil && strings.Contains(output, name)
}

func removeContainer(name string) error {
	return sh.RunV("docker", "rm", "-f", name)
}

func awaitContainer(name string, logSearch string) error {
	cxt, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	for {
		select {
		case <-cxt.Done():
			return errors.Errorf("timeout waiting for container %s to become ready", name)
		default:
			logs, err := sh.Output("docker", "logs", name)
			if err != nil {
				return errors.Wrapf(err, "could not get logs for container %s", name)
			}

			if strings.Contains(logs, logSearch) {
				return nil
			}

			if mg.Verbose() {
				fmt.Println(logs)
			}

			time.Sleep(time.Second)
		}
	}
}

func clean() error {
	err := os.RemoveAll("website/public")
	if err != nil {
		return errors.Wrap(err, "could not remove website/public")
	}

	if containerExists(containerName) {
		return removeContainer(containerName)
	}

	return nil
}
