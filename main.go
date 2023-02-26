package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/hashicorp/go-retryablehttp"
	"golang.org/x/mod/semver"
	"golang.org/x/sync/errgroup"

	"buf.build/gen/go/bufbuild/buf/bufbuild/connect-go/buf/alpha/registry/v1alpha1/registryv1alpha1connect"
	registryv1alpha1 "buf.build/gen/go/bufbuild/buf/protocolbuffers/go/buf/alpha/registry/v1alpha1"
)

const (
	apiPrefix    = "https://api."
	authTokenEnv = "BUF_TOKEN"
)

const (
	dateTimeLayout     string = "20060102150405"
	dateTimeLayoutZero string = "00000000000000"
)

func main() {
	log.SetFlags(0)

	if len(os.Args) != 3 {
		log.Fatalf("must provide exactly 2 arguments: plugin and module reference\nexample: remotever bufbuild/connect-es:latest buf.build/acme/petapis:latest")
	}
	pluginRef, err := newPluginRef(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	moduleRef, err := newModuleRef(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	var (
		pluginResp *pluginResp
		moduleResp *moduleResp
	)
	var g errgroup.Group
	g.Go(func() error {
		var err error
		pluginResp, err = resolvePlugin(ctx, pluginRef)
		return err
	})
	g.Go(func() error {
		var err error
		moduleResp, err = resolveModule(ctx, moduleRef)
		return err
	})
	if err := g.Wait(); err != nil {
		log.Fatal(err)
	}

	layout := dateTimeLayout
	if moduleResp.isDraft && pluginResp.registryType == registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_NPM {
		layout = dateTimeLayoutZero
	}
	syntheticVersion := fmt.Sprintf("%s-%s-%s.%d",
		pluginResp.version,
		moduleResp.commitCreateAt.UTC().Format(layout),
		moduleResp.commitName[:12],
		pluginResp.revision,
	)

	output := commandVersion{
		Version: syntheticVersion,
	}
	switch pluginResp.registryType {
	case registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_GO:
		goName := fmt.Sprintf("%s/%s/%s/%s", moduleRef.owner, moduleRef.name, pluginRef.owner, pluginRef.name)
		output.Command = fmt.Sprintf("go get %s/gen/go/%s@%s",
			moduleRef.remote,
			goName,
			syntheticVersion,
		)
		output.Hint = "Set GOPRIVATE=buf.build/gen/go\n\n" +
			"See https://docs.buf.build/bsr/remote-packages/go#private for more details."
	case registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_NPM:
		npmName := fmt.Sprintf("%s_%s.%s_%s", moduleRef.owner, moduleRef.name, pluginRef.owner, pluginRef.name)
		output.Command = fmt.Sprintf("npm install @buf/%s@%s", npmName, syntheticVersion)
		output.Hint = "Don't forget to update the registry config:\n\n\tnpm config set @buf:registry https://buf.build/gen/npm/v1/\n\n" +
			"For private modules you'll need to set the token:\n\n\tnpm config set //buf.build/gen/npm/v1/:_authToken {token}\n\n" +
			"See https://docs.buf.build/bsr/remote-packages/npm#private for more details."
	}

	by, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(by))
}

type pluginResp struct {
	version      string
	revision     uint32
	registryType registryv1alpha1.PluginRegistryType
}

// resolvePlugin returns the plugin version and revision.
func resolvePlugin(ctx context.Context, p *pluginRef) (*pluginResp, error) {
	apiURL := apiPrefix + p.remote
	retryClient := retryablehttp.NewClient()
	retryClient.Logger = nil
	client := registryv1alpha1connect.NewPluginCurationServiceClient(retryClient.StandardClient(), apiURL)

	var version string
	if p.version != "latest" {
		version = p.version
	}
	req := connect.NewRequest(&registryv1alpha1.GetLatestCuratedPluginRequest{
		Owner:    p.owner,
		Name:     p.name,
		Version:  version,
		Revision: 0, // Get latest revision only.
	})
	resp, err := client.GetLatestCuratedPlugin(ctx, req)
	if err != nil {
		return nil, err
	}
	plugin := resp.Msg.GetPlugin()
	switch plugin.RegistryType {
	case registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_GO, registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_NPM:
	default:
		return nil, fmt.Errorf("plugin is not compatible with BSR Remote Packages: registry type: %v", plugin.RegistryType.String())
	}
	return &pluginResp{
		version:      plugin.GetVersion(),
		revision:     plugin.GetRevision(),
		registryType: plugin.GetRegistryType(),
	}, nil
}

type moduleResp struct {
	commitName     string
	commitCreateAt time.Time
	isDraft        bool
}

func resolveModule(ctx context.Context, m *moduleRef) (*moduleResp, error) {
	apiURL := apiPrefix + m.remote
	retryClient := retryablehttp.NewClient()
	retryClient.Logger = nil
	client := registryv1alpha1connect.NewRepositoryCommitServiceClient(
		retryClient.StandardClient(),
		apiURL,
		connect.WithInterceptors(newAuthInterceptor()),
	)

	reference := m.reference
	if reference == "latest" {
		reference = "main"
	}
	req := connect.NewRequest(&registryv1alpha1.GetRepositoryCommitByReferenceRequest{
		RepositoryOwner: m.owner,
		RepositoryName:  m.name,
		Reference:       reference,
	})
	resp, err := client.GetRepositoryCommitByReference(ctx, req)
	if err != nil {
		log.Fatal(err)
	}
	commit := resp.Msg.GetRepositoryCommit()

	return &moduleResp{
		commitName:     commit.GetName(),
		commitCreateAt: commit.GetCreateTime().AsTime(),
		isDraft:        commit.GetDraftName() != "",
	}, nil
}

type moduleRef struct {
	remote string
	owner  string
	name   string
	// A reference can be either "latest" or a commit name, tag, or draft.
	reference string
}

func newModuleRef(s string) (*moduleRef, error) {
	name, ref, ok := strings.Cut(s, ":")
	if !ok {
		return nil, fmt.Errorf("must provide a module in the form of <module>:<version>")
	}
	ss := strings.Split(name, "/")
	if len(ss) != 3 {
		return nil, fmt.Errorf("must provide a plugin in the form of <remote>/<owner>/<name>")
	}
	if ref == "" {
		return nil, fmt.Errorf("must provide a valid module refeerence")
	}
	return &moduleRef{
		remote:    ss[0],
		owner:     ss[1],
		name:      ss[2],
		reference: ref,
	}, nil
}

type pluginRef struct {
	remote  string
	owner   string
	name    string
	version string
}

func newPluginRef(s string) (*pluginRef, error) {
	name, version, ok := strings.Cut(s, ":")
	if !ok {
		return nil, fmt.Errorf("must provide a plugin in the form of <plugin>:<version>")
	}
	ss := strings.Split(name, "/")
	if len(ss) != 2 {
		return nil, fmt.Errorf("must provide a plugin in the form of <owner>/<name>")
	}
	if version != "latest" {
		if !semver.IsValid(version) {
			return nil, fmt.Errorf("must provide a valid semver version")
		}
	}
	return &pluginRef{
		remote:  "buf.build",
		owner:   ss[0],
		name:    ss[1],
		version: version,
	}, nil
}

type commandVersion struct {
	Version string `json:"version"`
	Command string `json:"command"`
	Hint    string `json:"hint"`
}

func newAuthInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if token := os.Getenv(authTokenEnv); token != "" {
					req.Header().Set("Authorization", "Bearer "+token)
				}
				return next(ctx, req)
			})
		},
	)
}
