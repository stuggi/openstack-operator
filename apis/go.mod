module github.com/openstack-k8s-operators/openstack-operator/apis

go 1.19

require (
	github.com/openstack-k8s-operators/cinder-operator/api v0.0.0-20230227105511-fcd97b88b8f0
	github.com/openstack-k8s-operators/glance-operator/api v0.0.0-20230223111352-6181d5d15406
	github.com/openstack-k8s-operators/ironic-operator/api v0.0.0-20230223000829-9b83b8b2e63b
	github.com/openstack-k8s-operators/keystone-operator/api v0.0.0-20230227112334-13e05915d0e8
	github.com/openstack-k8s-operators/lib-common/modules/common v0.0.0-20230227110324-8f0c518c552b
	github.com/openstack-k8s-operators/mariadb-operator/api v0.0.0-20230227100533-e49b65b3e3df
	github.com/openstack-k8s-operators/neutron-operator/api v0.0.0-20230227101439-040b51c0c7d9
	github.com/openstack-k8s-operators/nova-operator/api v0.0.0-20230223144607-1b8be195d2b3
	github.com/openstack-k8s-operators/ovn-operator/api v0.0.0-20230302133719-f0aec2a30a89
	github.com/openstack-k8s-operators/ovs-operator/api v0.0.0-20230302151542-6877dc4718ae
	github.com/openstack-k8s-operators/placement-operator/api v0.0.0-20230223090011-71439754f993
	github.com/rabbitmq/cluster-operator v1.14.0
	k8s.io/apimachinery v0.26.1
	sigs.k8s.io/controller-runtime v0.14.4
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/emicklei/go-restful/v3 v3.10.1 // indirect
	github.com/evanphx/json-patch/v5 v5.6.0 // indirect
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/go-logr/logr v1.2.3 // indirect
	github.com/go-openapi/jsonpointer v0.19.6 // indirect
	github.com/go-openapi/jsonreference v0.20.1 // indirect
	github.com/go-openapi/swag v0.22.3 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/google/gnostic v0.6.9 // indirect
	github.com/google/go-cmp v0.5.9 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/gophercloud/gophercloud v1.2.0 // indirect
	github.com/imdario/mergo v0.3.13 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/openshift/api v3.9.0+incompatible // indirect
	github.com/openstack-k8s-operators/lib-common/modules/openstack v0.0.0-20230227110324-8f0c518c552b // indirect; indirect // indirect // indirect // indirect // indirect // indirect // indirect
	github.com/openstack-k8s-operators/lib-common/modules/storage v0.0.0-20230227110324-8f0c518c552b
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_golang v1.14.0 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.37.0 // indirect
	github.com/prometheus/procfs v0.8.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	golang.org/x/net v0.7.0 // indirect
	golang.org/x/oauth2 v0.0.0-20220909003341-f21342109be1 // indirect
	golang.org/x/sys v0.5.0 // indirect
	golang.org/x/term v0.5.0 // indirect
	golang.org/x/text v0.7.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	golang.org/x/tools v0.6.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.2.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/protobuf v1.28.1 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/api v0.26.1 // indirect // indirect
	k8s.io/apiextensions-apiserver v0.26.1 // indirect; indirect // indirect
	k8s.io/client-go v0.26.1 // indirect; indirect // indirect
	k8s.io/component-base v0.26.1 // indirect; indirect // indirect
	k8s.io/klog/v2 v2.80.1 // indirect
	k8s.io/kube-openapi v0.0.0-20230210211930-4b0756abdef5 // indirect; indirect // indirect // indirect // indirect // indirect
	k8s.io/utils v0.0.0-20230209194617-a36077c30491 // indirect; indirect // indirect // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect; indirect // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.2.3 // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)

//replace github.com/openstack-k8s-operators/keystone-operator/api => github.com/stuggi/keystone-operator/api v0.0.0-20230220093842-20ab299d925f

//replace github.com/openstack-k8s-operators/glance-operator/api => github.com/stuggi/glance-operator/api v0.0.0-20230217115439-5c7a300e51c1

//replace github.com/openstack-k8s-operators/cinder-operator/api => github.com/stuggi/cinder-operator/api v0.0.0-20230218094323-2331a92e888f

//replace github.com/openstack-k8s-operators/nova-operator/api => github.com/stuggi/nova-operator/api v0.0.0-20230217132719-333c46b53eb3

//replace github.com/openstack-k8s-operators/ovn-operator/api => github.com/stuggi/ovn-operator/api v0.0.0-20230221073113-fe697939fadf

//replace github.com/openstack-k8s-operators/neutron-operator/api => github.com/stuggi/neutron-operator/api v0.0.0-20230217133338-9ea12694ad8f

//replace github.com/openstack-k8s-operators/ovs-operator/api => github.com/stuggi/ovs-operator/api v0.0.0-20230221073210-0e0396244261

//replace github.com/openstack-k8s-operators/openstack-ansibleee-operator/api => github.com/stuggi/openstack-ansibleee-operator/api v0.0.0-20230227104036-4e22a96609c9

//replace github.com/openstack-k8s-operators/ovn-operator/api => github.com/stuggi/ovn-operator/api v0.0.0-20230302131340-c96aa547b128

//replace github.com/openstack-k8s-operators/ovs-operator/api => github.com/stuggi/ovs-operator/api v0.0.0-20230302134451-b4b975ab2c38

//replace github.com/openstack-k8s-operators/dataplane-operator/api => github.com/stuggi/dataplane-operator/api v0.0.0-20230228092551-af354beaeb97

//replace github.com/openstack-k8s-operators/openstack-ansibleee-operator/api => github.com/stuggi/openstack-ansibleee-operator/api v0.0.0-20230228091749-69b8aa7f5d94
