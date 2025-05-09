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
	"github.com/mfridman/go-kit/pkg/xflag"
)

const (
	apiPrefix     = "https://api."
	authTokenEnv  = "BUF_TOKEN"
	defaultRemote = "buf.build"
)

const (
	dateTimeLayout     string = "20060102150405"
	dateTimeLayoutZero string = "00000000000000"
)

func main() {
	_ = xflag.ParseToEnd(nil, nil)

	log.SetFlags(0)

	if len(os.Args) != 3 {
		log.Fatalf("must provide exactly 2 arguments: plugin and module reference\nexample: bsr-remotever bufbuild/connect-es:latest acme/petapis:latest")
	}
	pluginRef, err := newPluginRef(os.Args[1])
	if err != nil {
		log.Fatalf("failed to parse plugin reference: %v", err)
	}
	moduleRef, err := newModuleRef(os.Args[2])
	if err != nil {
		log.Fatalf("failed to parse module reference: %v", err)
	}
	if pluginRef.remote != moduleRef.remote {
		log.Fatalf("plugin and module must be from the same remote: %s != %s", pluginRef.remote, moduleRef.remote)
	}
	remote := pluginRef.remote
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
	if moduleResp.isDraft &&
		(pluginResp.registryType == registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_NPM ||
			pluginResp.registryType == registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_MAVEN) {
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
		output.Hint = fmt.Sprintf("Set GOPRIVATE=%s/gen/go\n\n", remote) +
			"See https://docs.buf.build/bsr/remote-packages/go#private for more details."
	case registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_NPM:
		npmName := fmt.Sprintf("%s_%s.%s_%s", moduleRef.owner, moduleRef.name, pluginRef.owner, pluginRef.name)
		output.Command = fmt.Sprintf("npm install @buf/%s@%s", npmName, syntheticVersion)
		output.Hint = fmt.Sprintf("Don't forget to update the registry config:\n\n\tnpm config set @buf:registry https://%s/gen/npm/v1/\n\n", remote) +
			fmt.Sprintf("For private modules you'll need to set the token:\n\n\tnpm config set //%s/gen/npm/v1/:_authToken {token}\n\n", remote) +
			"See https://docs.buf.build/bsr/remote-packages/npm#private for more details."
	case registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_MAVEN:
		syntheticVersion := fmt.Sprintf(
			"%s.%d.%s.%s",
			// pluginResp.version does not have the leading v, but semver expects the leading v.
			// Add it on, canonicalize it, and then remove it.
			strings.TrimPrefix(semver.Canonical(pluginResp.version), "v"),
			pluginResp.revision,
			moduleResp.commitCreateAt.UTC().Format(layout),
			moduleResp.commitName[:12],
		)
		output.Version = syntheticVersion
		// remoteComponents := strings.Split(remote, ".")
		// var groupID string
		// for i := len(remoteComponents) - 1; i >= 0; i-- {
		// 	groupID += remoteComponents[i] + "."
		// }
		// groupID += "gen"
		// mavenName := fmt.Sprintf("%s_%s_%s_%s", moduleRef.owner, moduleRef.name, pluginRef.owner, pluginRef.name)
		// No real one-liner for mvn/gradle, so omitting output.Command.
		output.Hint = "For private modules you'll need to update your Maven / Gradle configuration\n\nSee https://buf.build/docs/bsr/remote-packages/maven for more details"
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
	switch plugin.GetRegistryType() {
	case
		registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_GO,
		registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_NPM,
		registryv1alpha1.PluginRegistryType_PLUGIN_REGISTRY_TYPE_MAVEN:
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
		return nil, err
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
	// A module reference can be either "latest", "main", a commit name, tag, or draft.
	reference string
}

func newModuleRef(s string) (*moduleRef, error) {
	name, ref, ok := strings.Cut(s, ":")
	if !ok {
		return nil, fmt.Errorf("must provide a module in the form of <module>:<reference>")
	}
	remote, owner, name, err := parseRemoteOwnerName(name, "module")
	if err != nil {
		return nil, err
	}
	if ref == "" {
		return nil, fmt.Errorf("must provide a valid module reference")
	}
	return &moduleRef{
		remote:    remote,
		owner:     owner,
		name:      name,
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
	remote, owner, name, err := parseRemoteOwnerName(name, "plugin")
	if err != nil {
		return nil, err
	}
	if version != "latest" {
		if !semver.IsValid(version) {
			return nil, fmt.Errorf("must provide a valid semver version")
		}
	}
	return &pluginRef{
		remote:  remote,
		owner:   owner,
		name:    name,
		version: version,
	}, nil
}

type commandVersion struct {
	Version string `json:"version"`
	Command string `json:"command"`
	Hint    string `json:"hint"`
}

func newAuthInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token := os.Getenv(authTokenEnv); token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		})
	})
}

func parseRemoteOwnerName(remoteOwnerName string, refType string) (remote string, owner string, name string, err error) {
	ss := strings.Split(remoteOwnerName, "/")
	switch len(ss) {
	case 2:
		return defaultRemote, ss[0], ss[1], nil
	case 3:
		return ss[0], ss[1], ss[2], nil
	default:
		return "", "", "", fmt.Errorf("must provide a %s in the form of <remote>/<owner>/<name> or <owner>/<name>", refType)
	}
}
