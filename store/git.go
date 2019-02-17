/*
Copyright 2018 BlackRock, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package store

import (
	"fmt"
	"io/ioutil"

	"github.com/argoproj/argo-events/pkg/apis/sensor/v1alpha1"
	"golang.org/x/crypto/ssh"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/transport"
	"gopkg.in/src-d/go-git.v4/plumbing/transport/http"
	go_git_ssh "gopkg.in/src-d/go-git.v4/plumbing/transport/ssh"
	"k8s.io/client-go/kubernetes"
)

type GitArtifactReader struct {
	kubeClientset kubernetes.Interface
	artifact      *v1alpha1.GitArtifact
}

// NewGitReader returns a new git reader
func NewGitReader(kubeClientset kubernetes.Interface, gitArtifact *v1alpha1.GitArtifact) (*GitArtifactReader, error) {
	return &GitArtifactReader{
		kubeClientset: kubeClientset,
		artifact:      gitArtifact,
	}, nil
}

func getSSHKeyAuth(privateSshKeyFile string) (transport.AuthMethod, error) {
	var auth transport.AuthMethod
	sshKey, err := ioutil.ReadFile(privateSshKeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read ssh key file. err: %+v", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(sshKey))
	if err != nil {
		return nil, fmt.Errorf("failed to parse ssh key. err: %+v", err)
	}
	auth = &go_git_ssh.PublicKeys{User: "git", Signer: signer}
	return auth, nil
}

func (g *GitArtifactReader) getGitAuth() (transport.AuthMethod, error) {
	if g.artifact.Creds != nil {
		// retrieve access key id and secret access key
		username, err := GetSecrets(g.kubeClientset, g.artifact.Namespace, g.artifact.Creds.Username.Name, g.artifact.Creds.Username.Key)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve username: err: %+v", err)
		}
		password, err := GetSecrets(g.kubeClientset, g.artifact.Namespace, g.artifact.Creds.Password.Name, g.artifact.Creds.Password.Key)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve password: err: %+v", err)
		}
		return &http.BasicAuth{
			Username: username,
			Password: password,
		}, err
	}
	if g.artifact.SSHKeyPath != "" {
		return getSSHKeyAuth(g.artifact.SSHKeyPath)
	}
	return nil, nil
}

func (g *GitArtifactReader) readFromRepository(r *git.Repository) ([]byte, error) {
	w, err := r.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get working tree. err: %+v", err)
	}

	pullOpts := &git.PullOptions{
		RemoteName:        "origin",
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
	}

	auth, err := g.getGitAuth()
	if err != nil {
		return nil, err
	}
	if auth != nil {
		pullOpts.Auth = auth
	}

	refName, err := g.getBranchOrTag(r, g.artifact.Branch, g.artifact.Tag)
	if err != nil {
		return nil, err
	}
	if refName != nil {
		pullOpts.ReferenceName = *refName
	}

	if err := w.Pull(pullOpts); err != nil {
		return nil, fmt.Errorf("failed to pull latest updates. err: %+v", err)
	}

	file, err := w.Filesystem.Open(g.artifact.FilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open resource file. err: %+v", err)
	}

	var data []byte
	if _, err := file.Read(data); err != nil {
		return nil, fmt.Errorf("failed to read resource file. err: %+v", err)
	}

	return data, nil
}

func (g *GitArtifactReader) getBranchOrTag(r *git.Repository, branch, tag string) (*plumbing.ReferenceName, error) {
	if branch != "" {
		branch, err := r.Branch(branch)
		if err != nil {
			return nil, fmt.Errorf("branch %s not found. err: %+v", branch, err)
		}
		return &branch.Merge, nil
	}
	if tag != "" {
		tag, err := r.Tag(tag)
		if err != nil {
			return nil, fmt.Errorf("tag %s not found. err: %+v", tag, err)
		}
		refName := tag.Name()
		return &refName, nil
	}
	return nil, nil
}

func (g *GitArtifactReader) Read() ([]byte, error) {
	r, err := git.PlainOpen(g.artifact.CloneDirectory)
	if err != nil {
		if err == git.ErrRepositoryNotExists {
			cloneOpt := &git.CloneOptions{
				URL:               g.artifact.URL,
				RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
			}

			auth, err := g.getGitAuth()
			if err != nil {
				return nil, err
			}
			if auth != nil {
				cloneOpt.Auth = auth
			}

			refName, err := g.getBranchOrTag(r, g.artifact.Branch, g.artifact.Tag)
			if err != nil {
				return nil, err
			}
			if refName != nil {
				cloneOpt.ReferenceName = *refName
			}

			r, err := git.PlainClone(g.artifact.CloneDirectory, false, cloneOpt)
			if err != nil {
				return nil, fmt.Errorf("failed to clone repository. err: %+v", err)
			}
			return g.readFromRepository(r)
		}
		return nil, fmt.Errorf("failed to open repository. err: %+v", err)
	}
	return g.readFromRepository(r)
}
