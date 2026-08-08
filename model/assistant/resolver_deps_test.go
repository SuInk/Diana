package assistant

import "testing"

func TestResolverDependenciesProbes(t *testing.T) {
	deps := ResolverDependencies()
	if len(deps) != len(resolverDependencySpecs) {
		t.Fatalf("期望探测 %d 个依赖，实际 %d", len(resolverDependencySpecs), len(deps))
	}
	for _, dep := range deps {
		if dep.Name == "" || dep.Purpose == "" {
			t.Fatalf("依赖信息不完整：%+v", dep)
		}
		if dep.Available && dep.Path == "" {
			t.Fatalf("%s 可用但没有路径", dep.Name)
		}
		t.Logf("%s available=%v version=%q", dep.Name, dep.Available, dep.Version)
	}
}
