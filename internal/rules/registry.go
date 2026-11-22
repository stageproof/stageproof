package rules

import "github.com/stageproof/stageproof/internal/verdict"

// Rule is one public rule id. IDs are added and never renamed.
type Rule struct {
	ID          string
	Severity    verdict.Severity
	Title       string
	Explanation string
	Fix         string
	Group       string
}

// Groups are the findings-page sections, in page order.
const (
	GroupValues    = "values"
	GroupRendering = "rendering"
	GroupChanges   = "changes"
	GroupImages    = "images"
	GroupHelm4     = "helm4"
	GroupK8s       = "kubernetes"
	GroupImpact    = "impact"
	GroupLive      = "live"
)

// Rules returns every rule in docs order. The slice is the registry.
func Rules() []Rule {
	return rules
}

// Lookup returns the rule with this id.
func Lookup(id string) (Rule, bool) {
	for _, rule := range rules {
		if rule.ID == id {
			return rule, true
		}
	}
	return Rule{}, false
}

var rules = []Rule{
	{
		ID: "helm.values.silently_dropped", Severity: verdict.SeverityBlocker, Group: GroupValues,
		Title:       "A values key no longer has an effect",
		Explanation: "A key in your values file was used by the current chart and has no effect in the new chart. %%proof%% is %%render%% when two renders showed that, and %%templates%% when the reader decided. When you check a chart on its own, it means the chart does not use the key at all. The message does not print your value.",
		Fix:         "Rename the key to what the new chart uses, or remove it.",
	},
	{
		ID: "helm.values.renamed", Severity: verdict.SeverityWarning, Group: GroupValues,
		Title:       "A dropped key has a matching new key",
		Explanation: "A key that exists only in the new chart changes the same rendered fields as a dropped key. %%verified%% is true only when that effect matches. A similar name with a different effect stays false. The message names both keys and does not print your value.",
		Fix:         "Move the value to the key the report names when %%verified%% is true. Read the chart when it is false.",
	},
	{
		ID: "helm.values.default_changed", Severity: verdict.SeverityWarning, Group: GroupValues,
		Title:       "A chart default you do not set changed the render",
		Explanation: "A chart default you do not set changed, and rendering the new chart with the old default changes the objects. The message names the chart defaults. It does not print your values.",
		Fix:         "Set the key if you depended on the old default. With more than eight changes, re-run with %%--values-proof full%% for one line per key.",
	},
	{
		ID: "helm.values.preexisting_orphan", Severity: verdict.SeverityWarning, Group: GroupValues,
		Title:       "A values key is unused on both charts",
		Explanation: "A key in your values file is not used by the current chart and not by the new one.",
		Fix:         "Remove the key when convenient. The upgrade did not cause this.",
	},
	{
		ID: "helm.values.new_required_unset", Severity: verdict.SeverityBlocker, Group: GroupValues,
		Title:       "A newly required value is unset",
		Explanation: "The new chart requires a value that the current chart did not require, and you do not set it. A %%required%% call inside an %%if%% or an %%else%% is not this rule. The message does not include your values.",
		Fix:         "Set the value in your values file or with %%--set%%.",
	},
	{
		ID: "helm.values.parse_error", Severity: verdict.SeverityBlocker, Group: GroupValues,
		Title:       "A values file or a chart could not be read",
		Explanation: "A values file or a chart could not be read.",
		Fix:         "Fix the file that the message names.",
	},
	{
		ID: "helm.template.render_failed", Severity: verdict.SeverityBlocker, Group: GroupRendering,
		Title:       "The new chart did not render",
		Explanation: "%%helm template%% failed on the new chart with your values.",
		Fix:         "Read the Helm error in the report and fix the chart or the values.",
	},
	{
		ID: "helm.template.invalid_yaml", Severity: verdict.SeverityBlocker, Group: GroupRendering,
		Title:       "The render is not valid YAML",
		Explanation: "The new chart rendered, and the output is not valid YAML.",
		Fix:         "Fix the template that produces the invalid output.",
	},
	{
		ID: "manifest.invalid_yaml", Severity: verdict.SeverityBlocker, Group: GroupRendering,
		Title:       "The manifest path could not be read",
		Explanation: "%%--before%% or %%--after%% could not be read. The path is missing, the file is not %%.yaml%% or %%.yml%%, the directory has none of those files, or the YAML does not parse.",
		Fix:         "Point both flags at rendered manifests and fix the file named in the report.",
	},
	{
		ID: "helm.release.version_span", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "The chart version skips a minor or moves a major",
		Explanation: "The chart version skips more than one minor version, or moves to a new major version.",
		Fix:         "Read that project's upgrade notes before merging.",
	},
	{
		ID: "helm.release.app_version_changed", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "appVersion changed",
		Explanation: "%%appVersion%% in %%Chart.yaml%% changed. The image the chart runs often changes with it.",
		Fix:         "Confirm the application version is the one you meant to run.",
	},
	{
		ID: "helm.image.changed", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "An image in the render changed",
		Explanation: "An image in the render changed. That includes a container image, an %%image%% field on a custom resource, and an image passed as a container argument. A tag that only gains or loses a leading %%v%% is named. This warning does not itself ask the registry.",
		Fix:         "Read the image tags line for whether the new tag exists.",
	},
	{
		ID: "helm.operator.existing_custom_resources", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "Existing custom resources are outside this check",
		Explanation: "The chart ships custom resource definitions and the application it runs is changing. Existing custom resources are not in this check.",
		Fix:         "Plan the upgrade of the objects that already exist. A rehearsal uses an empty cluster.",
	},
	{
		ID: "helm.operator.documented_upgrade_behavior", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "A documented operator upgrade effect",
		Explanation: "The chart uses a custom resource group whose project documents an upgrade effect. The note and the source URL come from %%internal/knowledge/operators.yaml%%. %%helm.operator.primary_restart%% is the same note for one release.",
		Fix:         "Read the source named in the report. A rehearsal uses an empty cluster.",
	},
	{
		ID: "helm.operator.primary_restart", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "The same operator note, for one release",
		Explanation: "The same note as %%helm.operator.documented_upgrade_behavior%%, emitted again under the alias stored for that operator. The message and the source URL are the same. The alias is not a second kind of failure.",
		Fix:         "Read the source named in the report. A rehearsal uses an empty cluster.",
	},
	{
		ID: "helm.config.placeholder_used", Severity: verdict.SeverityInfo, Group: GroupChanges,
		Title:       "A placeholder was filled with a fixed sample",
		Explanation: "A %%${...}%% token outside the chart repository, name, or version had no entry in %%templatePlaceholders%%. It was replaced with %%stageproof-placeholder%%.",
		Fix:         "Add the token to %%templatePlaceholders%% when the real value matters.",
	},
	{
		ID: "helm.config.unresolved_placeholder", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "A chart address still contains a placeholder",
		Explanation: "A %%${...}%% token is still in the chart repository, name, or version. The result is %%NOT CHECKED%% and the report names the token.",
		Fix:         "Set that token in %%templatePlaceholders%%.",
	},
	{
		ID: "helm.dependency.not_updated", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "A dependency constraint changed without a new packaged chart",
		Explanation: "A %%Chart.yaml%% dependency constraint changed, and the packaged chart under %%charts/%% did not.",
		Fix:         "Run %%helm dependency update%% so the render uses the new chart.",
	},
	{
		ID: "config.changed_in_pr", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "The pull request edits the settings file",
		Explanation: "check-pr reads .stageproof.yaml from the base ref. A change in the pull request does not change ignores, policy, or the other settings for this check. The finding names the file and does not print the file.",
		Fix:         "Review that change on its own. An ignore added only in the pull request does not hide a finding.",
	},
	{
		ID: "config.ignore_expired", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "An ignore entry is past its date",
		Explanation: "An ignore whose expires date is before today no longer matches. The finding it named is reported as usual. The warning names the rule and the resource pattern.",
		Fix:         "Remove the entry, or set a new expires date in the file on the base branch.",
	},
	{
		ID: "flux.values.unreadable", Severity: verdict.SeverityWarning, Group: GroupChanges,
		Title:       "A valuesFrom object could not be read",
		Explanation: "A %%valuesFrom%% ConfigMap or Secret is not a file in this repository, its key is missing, or the Secret is encrypted. The chart is still rendered. The result stays %%PASSED%%, including after a rehearsal. The report names the reference and does not print the value.",
		Fix:         "Commit the object this HelmRelease names, or set %%optional: true%% when a missing object should be skipped. A missing key is still named.",
	},
	{
		ID: "supply.image.not_found", Severity: verdict.SeverityBlocker, Group: GroupImages,
		Title:       "The registry does not have this tag",
		Explanation: "The registry answered that the tag or digest is not there.",
		Fix:         "Use a tag the registry has, or remove the reference.",
	},
	{
		ID: "supply.image.platform_missing", Severity: verdict.SeverityBlocker, Group: GroupImages,
		Title:       "The manifest list has no linux/amd64 image",
		Explanation: "The tag is a manifest list and it has no %%linux/amd64%% manifest.",
		Fix:         "Publish that platform, or change the tag.",
	},
	{
		ID: "supply.image.unverifiable", Severity: verdict.SeverityInfo, Group: GroupImages,
		Title:       "The registry refused the tag lookup",
		Explanation: "The registry returned 401 or 403. A private image and a missing image can look the same.",
		Fix:         "Run where registry credentials are available, or pass %%--offline%% when this registry should not be asked.",
	},
	{
		ID: "supply.registry_unreachable", Severity: verdict.SeverityInfo, Group: GroupImages,
		Title:       "The registry did not answer",
		Explanation: "The request timed out, or the registry returned a rate limit or a server error.",
		Fix:         "Run the check again. This is not proof that the tag is missing.",
	},
	{
		ID: "supply.image.moved", Severity: verdict.SeverityInfo, Group: GroupImages,
		Title:       "The image repository changed",
		Explanation: "The registry or the repository changed. The message names both repositories.",
		Fix:         "Confirm the new repository is the one you meant.",
	},
	{
		ID: "supply.chart.deprecated", Severity: verdict.SeverityWarning, Group: GroupImages,
		Title:       "The chart is marked deprecated",
		Explanation: "The new chart's %%Chart.yaml%% sets %%deprecated: true%%.",
		Fix:         "Read the chart's notes before staying on it.",
	},
	{
		ID: "helm4.chart_v3", Severity: verdict.SeverityWarning, Group: GroupHelm4,
		Title:       "The chart apiVersion is v3",
		Explanation: "The new chart's %%Chart.yaml%% %%apiVersion%% is %%v3%%. Charts v3 are experimental.",
		Fix:         "Keep %%apiVersion: v2%% until you mean to use the experimental format. See [[Moving to Helm 4|#helm4]].",
	},
	{
		ID: "helm4.render_differs", Severity: verdict.SeverityWarning, Group: GroupHelm4,
		Title:       "Two Helm binaries rendered different objects",
		Explanation: "%%--helm-compare%% rendered different objects. The message names the objects and paths. A field that changes between two renders of the same binary is ignored.",
		Fix:         "Read the path. The report does not print the value.",
	},
	{
		ID: "helm4.render_failed", Severity: verdict.SeverityBlocker, Group: GroupHelm4,
		Title:       "One Helm binary could not render the chart",
		Explanation: "One of the two helm binaries could not render the chart.",
		Fix:         "Read the helm error in the report. Fix the chart, or stay on the helm that rendered it.",
	},
	{
		ID: "k8s.api.removed", Severity: verdict.SeverityBlocker, Group: GroupK8s,
		Title:       "The chart uses an API this Kubernetes version removed",
		Explanation: "A resource uses an API version that the Kubernetes version in %%--current-k8s%% has removed.",
		Fix:         "Move the resource to the API version named in the report.",
	},
	{
		ID: "k8s.api.eol_blocker", Severity: verdict.SeverityWarning, Group: GroupK8s,
		Title:       "The next Kubernetes version removes this API",
		Explanation: "A resource uses an API version that works today and is removed in the version in %%--next-k8s%%.",
		Fix:         "Migrate the resource before you upgrade the cluster.",
	},
	{
		ID: "k8s.api.deprecated", Severity: verdict.SeverityWarning, Group: GroupK8s,
		Title:       "The API is deprecated on this Kubernetes version",
		Explanation: "A resource uses an API version that is deprecated on the version in %%--current-k8s%%.",
		Fix:         "Plan the migration. The report names the version in which the API is removed.",
	},
	{
		ID: "k8s.upgrade.immutable_selector_changed", Severity: verdict.SeverityBlocker, Group: GroupK8s,
		Title:       "A workload selector changed",
		Explanation: "The label selector of a Deployment, StatefulSet, or DaemonSet differs between the current and the new chart. Kubernetes does not allow that change on an existing workload.",
		Fix:         "Keep the selector as it was, or plan to delete and recreate the workload.",
	},
	{
		ID: "k8s.apply.immutable_field", Severity: verdict.SeverityBlocker, Group: GroupK8s,
		Title:       "The API server rejected an update",
		Explanation: "The local API server rejected an update. The report names the field. A Job pod template and a StatefulSet %%volumeClaimTemplates%% change are this rule. A Helm hook is dry-run as a create, not as an update.",
		Fix:         "Keep the field, or plan to delete and recreate the object. On %%--next-k8s%% the same rejection is a warning.",
	},
	{
		ID: "k8s.apply.unknown_field", Severity: verdict.SeverityWarning, Group: GroupK8s,
		Title:       "The object has a field the schema does not declare",
		Explanation: "The object has a field the schema does not declare. Helm 3 drops it and the upgrade succeeds. A new Helm 4 install uses server-side apply and rejects it, and so does %%--server-side-apply%%. Those two cases are %%ERROR%%. An upgrade of a release Helm 3 created stays a warning.",
		Fix:         "Remove the field, or keep it only if your apply path drops unknown fields.",
	},
	{
		ID: "k8s.apply.invalid", Severity: verdict.SeverityBlocker, Group: GroupK8s,
		Title:       "The API server rejected the object",
		Explanation: "The API server rejected the object. An unsupported enum value is this rule. The report names the field and the constraint. It does not print the rejected value. On %%--next-k8s%% this is a warning.",
		Fix:         "Change the object so that Kubernetes version accepts it.",
	},
	{
		ID: "crd.existing_object_invalid", Severity: verdict.SeverityBlocker, Group: GroupK8s,
		Title:       "An existing object does not match the new CRD",
		Explanation: "An object from the current chart does not match the new CRD schema. The same rule covers a storage version that was removed, and a field an existing custom resource uses when that field was removed, changed type, became required, or lost an enum value. The API server can also emit this rule.",
		Fix:         "Keep the stored object valid, or plan a migration before the upgrade.",
	},
	{
		ID: "k8s.apply.not_exercised", Severity: verdict.SeverityInfo, Group: GroupK8s,
		Title:       "An object was not sent to the API server",
		Explanation: "A webhook configuration, an APIService, or an object the server could not judge was not sent. Sending a webhook configuration would make later requests fail.",
		Fix:         "Nothing, unless you needed that object judged. It is not a failure.",
	},
	{
		ID: "k8s.crd.oversized_annotation_limit", Severity: verdict.SeverityWarning, Group: GroupK8s,
		Title:       "A CRD is larger than 256 KiB",
		Explanation: "A CRD in the chart is larger than 256 KiB.",
		Fix:         "See [[Large CRDs and server-side apply|#large-crds]].",
	},
	{
		ID: "impact.delete.crd", Severity: verdict.SeverityBlocker, Group: GroupImpact,
		Title:       "A templated CRD is dropped",
		Explanation: "A templated custom resource definition is in the current chart and not in the new one. Helm deletes it, and Kubernetes then deletes every object of that kind. An object with %%helm.sh/resource-policy: keep%% is not this rule.",
		Fix:         "Keep rendering the CRD, or move the objects before the upgrade.",
	},
	{
		ID: "impact.delete.pvc", Severity: verdict.SeverityBlocker, Group: GroupImpact,
		Title:       "A PersistentVolumeClaim is dropped",
		Explanation: "A PersistentVolumeClaim is dropped. The data on that claim goes with it. The keep annotation suppresses this.",
		Fix:         "Keep the claim in the chart, or copy the data first.",
	},
	{
		ID: "impact.delete.namespace", Severity: verdict.SeverityBlocker, Group: GroupImpact,
		Title:       "A Namespace is dropped",
		Explanation: "A Namespace is dropped. Every object in it is deleted with it. The keep annotation suppresses this.",
		Fix:         "Keep the namespace in the chart.",
	},
	{
		ID: "impact.delete.statefulset", Severity: verdict.SeverityBlocker, Group: GroupImpact,
		Title:       "A StatefulSet is dropped",
		Explanation: "A StatefulSet is dropped and %%persistentVolumeClaimRetentionPolicy.whenDeleted%% is %%Delete%%, so its claims are deleted too. When that field is not %%Delete%%, the same rule is a warning.",
		Fix:         "Keep the StatefulSet, or accept that its claims will be deleted.",
	},
	{
		ID: "impact.delete", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "Other objects are dropped",
		Explanation: "Other objects the new chart does not render. When a near-identical object is created under a new name, the message starts with %%replaced: delete then create%%. An object with %%helm.sh/hook%% is not listed, and it is not %%impact.restart%%.",
		Fix:         "Confirm the objects are meant to go.",
	},
	{
		ID: "crd.scope_changed", Severity: verdict.SeverityBlocker, Group: GroupImpact,
		Title:       "A CRD changed scope",
		Explanation: "A custom resource definition changed from namespaced to cluster scope, or the other way around.",
		Fix:         "Do not change scope on a CRD that already has objects.",
	},
	{
		ID: "crd.schema.incompatible", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A CRD schema changed and no current object uses it",
		Explanation: "A field was removed, changed type, became required, or lost an enum value, and no current object of that version uses it. A newly required field the object already sets, and an enum value that is still allowed, stay here. It is %%crd.existing_object_invalid%% when a current object of that version is missing a new required field, keeps a removed enum value, or uses another broken field. Each such object is named.",
		Fix:         "Read the field the report names before you upgrade.",
	},
	{
		ID: "crd.not_upgraded_by_helm", Severity: verdict.SeverityBlocker, Group: GroupImpact,
		Title:       "A CRD under crds/ changed and Helm will not upgrade it",
		Explanation: "A CRD under %%crds/%% changed in a way the new schema will not accept. Plain Helm does not upgrade CRDs in that directory. Argo CD with %%skipCrds: true%% uses this same rule. A new file, a removed file, or a compatible edit is a warning.",
		Fix:         "Apply that CRD yourself before the upgrade, or keep the schema compatible.",
	},
	{
		ID: "crd.argocd_renders_crds", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "Argo CD renders CRDs from the chart",
		Explanation: "An Argo CD check. %%spec.source.helm.skipCrds: false%% means Argo CD renders with %%helm template%% and applies CRDs in %%crds/%%. When the field is absent, the warning says the input does not say. %%skipCrds: true%% is %%crd.not_upgraded_by_helm%% instead.",
		Fix:         "Set %%skipCrds%% to match the Application. An incompatible CRD with %%skipCrds: true%% has to be applied before the upgrade.",
	},
	{
		ID: "crd.flux_upgrade_crds", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "Flux names how CRDs in crds/ are upgraded",
		Explanation: "A Flux HelmRelease. %%spec.upgrade.crds%% omitted or %%Skip%% means CRDs under %%crds/%% are neither installed nor upgraded. An incompatible change is %%ERROR%%. %%Create%% installs a new CRD and leaves an existing one. %%CreateReplace%% is info, and the schema is compared. The helm-controller type comment says the upgrade default is Skip. The user guide lists Create for both install and upgrade. This check follows the type comment.",
		Fix:         "Set %%upgrade.crds%% to match the HelmRelease. %%CreateReplace%% updates an existing CRD and does not delete it.",
	},
	{
		ID: "impact.privilege.rbac", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A Role or ClusterRole gained rules",
		Explanation: "A Role or ClusterRole gained rules. Wildcards, a cluster-wide secrets read, %%escalate%%, %%bind%%, %%impersonate%%, and %%pods/exec%% are named when present. Any new rule is a warning.",
		Fix:         "Confirm the new verbs are intended.",
	},
	{
		ID: "impact.privilege.binding", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A cluster-admin binding grew",
		Explanation: "A new binding to %%cluster-admin%%, or a new subject on a binding that already points at %%cluster-admin%%.",
		Fix:         "Confirm who gained that access.",
	},
	{
		ID: "impact.privilege.securitycontext", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A security context became weaker",
		Explanation: "A pod or container security context became weaker, including when the whole block is removed: privileged, privilege escalation, running as root, a writable root filesystem, or a capability that was added or no longer dropped.",
		Fix:         "Keep the stricter context unless the process needs the new right.",
	},
	{
		ID: "impact.privilege.hostpath", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A hostPath volume was added",
		Explanation: "A new hostPath volume, named by the volume name. The path is not printed.",
		Fix:         "Confirm the workload should see that host path.",
	},
	{
		ID: "impact.privilege.webhook", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A failing admission webhook has no namespace selector",
		Explanation: "A new admission webhook with %%failurePolicy: Fail%%, or an empty failure policy, and no namespace selector.",
		Fix:         "Add a namespace selector, or accept that a webhook outage fails every request.",
	},
	{
		ID: "impact.privilege.conversion", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "CRD conversion now uses a webhook",
		Explanation: "A CRD %%conversion.strategy%% became %%Webhook%%.",
		Fix:         "The conversion service has to be up before objects of that kind are read.",
	},
	{
		ID: "impact.exposure.service", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A Service is now reachable from outside",
		Explanation: "A Service type moved to %%LoadBalancer%% or %%NodePort%%.",
		Fix:         "Confirm the Service should be reachable from outside the cluster.",
	},
	{
		ID: "impact.exposure.route", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A route gained a host",
		Explanation: "An Ingress, Gateway, HTTPRoute, GRPCRoute, or TLSRoute gained a host.",
		Fix:         "Confirm the host should be published.",
	},
	{
		ID: "impact.exposure.networkpolicy", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "A NetworkPolicy was removed or opened",
		Explanation: "A NetworkPolicy was removed or opened to all. A removed policy is not also listed under %%impact.delete%%.",
		Fix:         "Keep the policy, or accept the wider access.",
	},
	{
		ID: "impact.exposure.host", Severity: verdict.SeverityWarning, Group: GroupImpact,
		Title:       "The pod uses the host network",
		Explanation: "%%hostNetwork%% or a %%hostPort%% was added.",
		Fix:         "Confirm the pod should use the host network.",
	},
	{
		ID: "impact.restart", Severity: verdict.SeverityInfo, Group: GroupImpact,
		Title:       "A pod template changed",
		Explanation: "A pod template changed. The report names the field. A restart is normal for an upgrade.",
		Fix:         "Nothing, unless the field is not the change you expected.",
	},
	{
		ID: "impact.capacity.replicas", Severity: verdict.SeverityInfo, Group: GroupImpact,
		Title:       "The replica count went down",
		Explanation: "Replicas went down. The numbers are not printed.",
		Fix:         "Confirm the smaller count is intended.",
	},
	{
		ID: "impact.capacity.requests", Severity: verdict.SeverityInfo, Group: GroupImpact,
		Title:       "A resource request went up",
		Explanation: "A cpu or memory request went up. The quantities are not printed.",
		Fix:         "Confirm the cluster has the extra capacity.",
	},
	{
		ID: "impact.capacity.pdb", Severity: verdict.SeverityInfo, Group: GroupImpact,
		Title:       "A disruption budget changed",
		Explanation: "%%minAvailable%% or %%maxUnavailable%% changed. The report names the field.",
		Fix:         "Confirm disruptions are still allowed during the upgrade.",
	},
	{
		ID: "impact.capacity.hpa", Severity: verdict.SeverityInfo, Group: GroupImpact,
		Title:       "An autoscaler bound changed",
		Explanation: "%%minReplicas%% or %%maxReplicas%% changed. The report names the field.",
		Fix:         "Confirm the new bounds.",
	},
	{
		ID: "helm.runtime.upgrade_failed", Severity: verdict.SeverityBlocker, Group: GroupLive,
		Title:       "The rehearsal upgrade failed",
		Explanation: "The current chart was healthy, and %%helm upgrade%% to the new chart failed.",
		Fix:         "Read the helm error in the report. The current release was healthy before this step.",
	},
	{
		ID: "helm.runtime.pod_restarts", Severity: verdict.SeverityBlocker, Group: GroupLive,
		Title:       "A pod was not ready after the upgrade",
		Explanation: "The upgrade finished, and afterwards a pod was not ready or a container had restarted.",
		Fix:         "Read which pod the report names. The upgrade command itself had already finished.",
	},
	{
		ID: "helm.runtime.rollback_failed", Severity: verdict.SeverityBlocker, Group: GroupLive,
		Title:       "The rehearsal rollback failed",
		Explanation: "%%helm rollback%% to the first revision failed.",
		Fix:         "Read the helm error in the report. The rollback to the first revision did not finish.",
	},
	{
		ID: "helm.runtime.helm_test_failed", Severity: verdict.SeverityBlocker, Group: GroupLive,
		Title:       "helm test failed after the upgrade",
		Explanation: "The chart's %%helm test%% hooks failed after the upgrade. Only with %%--helm-test%%.",
		Fix:         "Read the hook output in the report. This rule is recorded only when %%--helm-test%% is set.",
	},
	{
		ID: "live.smoke_failed", Severity: verdict.SeverityBlocker, Group: GroupLive,
		Title:       "A smoke test failed after the upgrade",
		Explanation: "A command from %%smoke%% in the base %%.stageproof.yaml%% failed or timed out after the upgrade and before the rollback. The message names the test. The detail is the end of its output, with your values removed. %%check-pr%% does not run a %%smoke%% entry added only in the pull request.",
		Fix:         "Read the output in the report. The command runs in the temporary cluster, with %%STAGEPROOF_NAMESPACE%% and %%STAGEPROOF_RELEASE%% set.",
	},
	{
		ID: "helm.crd.upgrade_failed", Severity: verdict.SeverityBlocker, Group: GroupLive,
		Title:       "The rehearsal could not apply chart CRDs",
		Explanation: "The CRDs in the new chart's %%crds%% directory could not be applied to the cluster.",
		Fix:         "Read the apply error in the report. Those CRDs were not accepted.",
	},
}
