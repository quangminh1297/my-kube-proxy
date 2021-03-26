/*
Copyright 2014 The Kubernetes Authors.

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

package userspace

import (
	"context"
	"errors"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	//clientset "k8s.io/client-go/deprecated"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/proxy"
	"k8s.io/kubernetes/pkg/proxy/util"
	nodeutil "k8s.io/kubernetes/pkg/util/node"
	"k8s.io/kubernetes/pkg/util/slice"
	"net"
	"reflect"
	"sync"
	"time"
)

var (
	ErrMissingServiceEntry = errors.New("missing service entry")
	ErrMissingEndpoints    = errors.New("missing endpoints")
)

var alert_tmp string

type affinityState struct {
	clientIP string
	//clientProtocol  api.Protocol //not yet used
	//sessionCookie   string       //not yet used
	endpoint string
	lastUsed time.Time
}

type affinityPolicy struct {
	affinityType v1.ServiceAffinity
	affinityMap  map[string]*affinityState // map client IP -> affinity info
	ttlSeconds   int
}

// LoadBalancerRR is a round-robin load balancer.
type LoadBalancerRR struct {
	lock     sync.RWMutex
	services map[proxy.ServicePortName]*balancerState
}

// Ensure this implements LoadBalancer.
var _ LoadBalancer = &LoadBalancerRR{}

type balancerState struct {
	endpoints []string // a list of "ip:port" style strings
	index     int      // current index into endpoints
	affinity  affinityPolicy
	/*My-proxy-LOCALY*/
	localendpoints []string // a list of local endpoints
	localindex     int      // current index into localendpoints
	super_endpoints map[portsToNodeNames[portname]]supertypes
}

type supertypes struct {
	supersupertype []string
}

func newAffinityPolicy(affinityType v1.ServiceAffinity, ttlSeconds int) *affinityPolicy {
	return &affinityPolicy{
		affinityType: affinityType,
		affinityMap:  make(map[string]*affinityState),
		ttlSeconds:   ttlSeconds,
	}
}

// NewLoadBalancerRR returns a new LoadBalancerRR.
func NewLoadBalancerRR() *LoadBalancerRR {
	return &LoadBalancerRR{
		services: map[proxy.ServicePortName]*balancerState{},
	}
}

func (lb *LoadBalancerRR) NewService(svcPort proxy.ServicePortName, affinityType v1.ServiceAffinity, ttlSeconds int) error {
	klog.V(4).Infof("LoadBalancerRR NewService %q", svcPort)
	lb.lock.Lock()
	defer lb.lock.Unlock()
	lb.newServiceInternal(svcPort, affinityType, ttlSeconds)
	return nil
}

// This assumes that lb.lock is already held.
func (lb *LoadBalancerRR) newServiceInternal(svcPort proxy.ServicePortName, affinityType v1.ServiceAffinity, ttlSeconds int) *balancerState {
	if ttlSeconds == 0 {
		ttlSeconds = int(v1.DefaultClientIPServiceAffinitySeconds) //default to 3 hours if not specified.  Should 0 be unlimited instead????
	}

	if _, exists := lb.services[svcPort]; !exists {
		lb.services[svcPort] = &balancerState{affinity: *newAffinityPolicy(affinityType, ttlSeconds)}
		klog.V(4).Infof("LoadBalancerRR service %q did not exist, created", svcPort)
	} else if affinityType != "" {
		lb.services[svcPort].affinity.affinityType = affinityType
	}
	return lb.services[svcPort]
}

func (lb *LoadBalancerRR) DeleteService(svcPort proxy.ServicePortName) {
	klog.V(4).Infof("LoadBalancerRR DeleteService %q", svcPort)
	lb.lock.Lock()
	defer lb.lock.Unlock()
	delete(lb.services, svcPort)
}

// return true if this service is using some form of session affinity.
func isSessionAffinity(affinity *affinityPolicy) bool {
	// Should never be empty string, but checking for it to be safe.
	if affinity.affinityType == "" || affinity.affinityType == v1.ServiceAffinityNone {
		return false
	}
	return true
}

// ServiceHasEndpoints checks whether a service entry has endpoints.
func (lb *LoadBalancerRR) ServiceHasEndpoints(svcPort proxy.ServicePortName) bool {
	lb.lock.RLock()
	defer lb.lock.RUnlock()
	state, exists := lb.services[svcPort]
	// TODO: while nothing ever assigns nil to the map, *some* of the code using the map
	// checks for it.  The code should all follow the same convention.
	return exists && state != nil && len(state.endpoints) > 0
}

// NextEndpoint returns a service endpoint.
//// The service endpoint is chosen using the round-robin algorithm.
/**********************************************************************************/
/////**********************************************************************************/
//func (lb *LoadBalancerRR) NextEndpoint(svcPort proxy.ServicePortName, srcAddr net.Addr, sessionAffinityReset bool) (string, error) {
//	// Coarse locking is simple.  We can get more fine-grained if/when we
//	// can prove it matters.
//	lb.lock.Lock()
//	defer lb.lock.Unlock()
//
//
//	/*** services[svcPort] using for get endpoint following namespace*/
//	state, exists := lb.services[svcPort]
//	if !exists || state == nil {
//		return "", ErrMissingServiceEntry
//	}
//	if len(state.endpoints) == 0 {
//		return "", ErrMissingEndpoints
//	}
//
//	klog.V(0).Infof("NextEndpoint for service %q, srcAddr=%v: endpoints: %+v", svcPort, srcAddr, state.endpoints)
//
//	sessionAffinityEnabled := isSessionAffinity(&state.affinity)
//
//	/**/klog.V(0).Infof("CHECK <<< sessionAffinityEnabled >>> --> ", sessionAffinityEnabled)
//
//	var ipaddr string
//
//	/**/klog.V(0).Infof("CHECK <<< ipaddr >>> --> ", ipaddr)
//
//	if sessionAffinityEnabled {
//		// Caution: don't shadow ipaddr
//		var err error
//		ipaddr, _, err = net.SplitHostPort(srcAddr.String())
//		if err != nil {
//			return "", fmt.Errorf("malformed source address %q: %v", srcAddr.String(), err)
//		}
//		if !sessionAffinityReset {
//			sessionAffinity, exists := state.affinity.affinityMap[ipaddr]
//			if exists && int(time.Since(sessionAffinity.lastUsed).Seconds()) < state.affinity.ttlSeconds {
//				// Affinity wins.
//				endpoint := sessionAffinity.endpoint
//				sessionAffinity.lastUsed = time.Now()
//				klog.V(0).Infof("NextEndpoint for service %q from IP %s with sessionAffinity %#v: %s", svcPort, ipaddr, sessionAffinity, endpoint)
//				return endpoint, nil
//			}
//		}
//	}
//	// Take the next endpoint.
//	endpoint := state.endpoints[state.index]
//	state.index = (state.index + 1) % len(state.endpoints)
//
//	if sessionAffinityEnabled {
//		var affinity *affinityState
//		affinity = state.affinity.affinityMap[ipaddr]
//		if affinity == nil {
//			affinity = new(affinityState) //&affinityState{ipaddr, "TCP", "", endpoint, time.Now()}
//			state.affinity.affinityMap[ipaddr] = affinity
//		}
//		affinity.lastUsed = time.Now()
//		affinity.endpoint = endpoint
//		affinity.clientIP = ipaddr
//		klog.V(0).Infof("Updated affinity key %s: %#v", ipaddr, state.affinity.affinityMap[ipaddr])
//	}
//
//	return endpoint, nil
//}
/**********************************************************************************/
/**********************************************************************************/
/**********************************************************************************/


func (lb *LoadBalancerRR) NextEndpoint_V2(svcPort proxy.ServicePortName, srcAddr net.Addr, sessionAffinityReset bool) (string, error) {
	// Coarse locking is simple.  We can get more fine-grained if/when we
	// can prove it matters.
	lb.lock.Lock()
	defer lb.lock.Unlock()

	/*** services[svcPort] using for get endpoint following namespace*/
	state, exists := lb.services[svcPort]

	if !exists || state == nil {
		return "", ErrMissingServiceEntry
	}
	if len(state.endpoints) == 0 {
		return "", ErrMissingEndpoints
	}
	klog.V(0).Infof("NextEndpoint for service %q, srcAddr=%v: endpoints: %+v", svcPort, srcAddr, state.endpoints)

	IneedThat := "NoNeed"
	if IneedThat == "Need" {
		/* START MOD-01*/
		Config, _ := clientcmd.BuildConfigFromFlags("", "/home/config")
		clientset, _ := kubernetes.NewForConfig(Config)

		nodes, _ := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		nodeip := []v1.NodeAddress{}
		for i := 0; i < len(nodes.Items); i++ {
			nodeip = nodes.Items[i].Status.Addresses
			/**/ klog.V(0).Infof("CHECK <<< nodeip - AFTER >>> --> ", nodeip)
			/**/ klog.V(0).Infof("CHECK <<< type of nodeip - AFTER >>> --> ", reflect.TypeOf(nodeip))
			/**/ klog.V(0).Infof("CHECK <<< len(nodes.Items) >>> --> ", len(nodes.Items))
		}
		/* Label01-MOD-01*/
		pods, _ := clientset.CoreV1().Pods(svcPort.Namespace).List(context.TODO(), metav1.ListOptions{})
		for _, pod := range pods.Items {
			//Status
			/**/ klog.V(0).Infof("CHECK <<< PodIP - PodIP >>> --> ", pod.Status.PodIP)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - HostIP >>> --> ", pod.Status.HostIP)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - ContainerStatuses >>> --> ", pod.Status.ContainerStatuses)

			//Spec
			/**/ klog.V(0).Infof("CHECK <<< PodIP - NodeName >>> --> ", pod.Spec.NodeName)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - NodeSelector >>> --> ", pod.Spec.NodeSelector)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - Containers >>> --> ", pod.Spec.Containers)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - PriorityClassName >>> --> ", pod.Spec.PriorityClassName)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - Priority >>> --> ", pod.Spec.Priority)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - Subdomain >>> --> ", pod.Spec.Subdomain)
			/**/ klog.V(0).Infof("CHECK <<< PodIP - TopologySpreadConstraints >>> --> ", pod.Spec.TopologySpreadConstraints)
			/**/ klog.V(0).Infof("PODS.Items >>> --> ", len(pods.Items))
		}
		/*End MOD-01-V1*/
		//minhmap := make(map[int]string)
		//for i := 0; i < len(state.endpoints); i++ {
		//	minhmap[state.index] = state.endpoints[state.index]
		//}
	}

	sessionAffinityEnabled := isSessionAffinity(&state.affinity)

	var ipaddr string
	if sessionAffinityEnabled {
		// Caution: don't shadow ipaddr
		var err error
		ipaddr, _, err = net.SplitHostPort(srcAddr.String())
		if err != nil {
			return "", fmt.Errorf("malformed source address %q: %v", srcAddr.String(), err)
		}
		if !sessionAffinityReset {
			sessionAffinity, exists := state.affinity.affinityMap[ipaddr]
			if exists && int(time.Since(sessionAffinity.lastUsed).Seconds()) < state.affinity.ttlSeconds {
				// Affinity wins.
				endpoint := sessionAffinity.endpoint
				sessionAffinity.lastUsed = time.Now()
				klog.V(0).Infof("NextEndpoint for service %q from IP %s with sessionAffinity %#v: %s", svcPort, ipaddr, sessionAffinity, endpoint)
				/**/klog.V(0).Infof("CHECK <<< svcPort - sessionAffinityEnabled >>> --> ", svcPort)
				/**/klog.V(0).Infof("CHECK <<< ipaddr - sessionAffinityEnabled >>> --> ", ipaddr)
				/**/klog.V(0).Infof("CHECK <<< sessionAffinity - sessionAffinityEnabled >>> --> ", sessionAffinity)
				/**/klog.V(0).Infof("CHECK <<< endpoints - sessionAffinityEnabled >>> --> ", endpoint)
				return endpoint, nil
			}
		}
	}
	// Take the next endpoint.
	/**/klog.V(0).Infof("<<< BREAK POINT 01 >>>")

	//endpoint := state.endpoints[state.index]
	//state.index = (state.index + 1) % len(state.endpoints)

	/**LOCALY CODE**/
	var endpoint string
	if len(state.localendpoints) == 0 {
		endpoint = state.endpoints[state.index]
		state.index = (state.index + 1) % len(state.endpoints)
	} else {
		endpoint = state.localendpoints[state.localindex]
		state.localindex = (state.localindex + 1) % len(state.localendpoints)
	}
	/*END*/

	if sessionAffinityEnabled {
		var affinity *affinityState
		affinity = state.affinity.affinityMap[ipaddr]
		if affinity == nil {
			affinity = new(affinityState) //&affinityState{ipaddr, "TCP", "", endpoint, time.Now()}
			state.affinity.affinityMap[ipaddr] = affinity
		}
		affinity.lastUsed = time.Now()
		affinity.endpoint = endpoint
		affinity.clientIP = ipaddr
		klog.V(0).Infof("Updated affinity key %s: %#v", ipaddr, state.affinity.affinityMap[ipaddr])
	}

	return endpoint, nil
}

// Remove any session affinity records associated to a particular endpoint (for example when a pod goes down).
func removeSessionAffinityByEndpoint(state *balancerState, svcPort proxy.ServicePortName, endpoint string) {
	for _, affinity := range state.affinity.affinityMap {
		if affinity.endpoint == endpoint {
			klog.V(4).Infof("Removing client: %s from affinityMap for service %q", affinity.endpoint, svcPort)
			delete(state.affinity.affinityMap, affinity.clientIP)
		}
	}
}

// Loop through the valid endpoints and then the endpoints associated with the Load Balancer.
// Then remove any session affinity records that are not in both lists.
// This assumes the lb.lock is held.
func (lb *LoadBalancerRR) removeStaleAffinity(svcPort proxy.ServicePortName, newEndpoints []string) {
	newEndpointsSet := sets.NewString()
	for _, newEndpoint := range newEndpoints {
		newEndpointsSet.Insert(newEndpoint)
	}

	state, exists := lb.services[svcPort]
	if !exists {
		return
	}
	for _, existingEndpoint := range state.endpoints {
		if !newEndpointsSet.Has(existingEndpoint) {
			klog.V(2).Infof("Delete endpoint %s for service %q", existingEndpoint, svcPort)
			removeSessionAffinityByEndpoint(state, svcPort, existingEndpoint)
		}
	}
}

func (lb *LoadBalancerRR) OnEndpointsAdd(endpoints *v1.Endpoints) {
	portsToEndpoints := util.BuildPortsToEndpointsMap(endpoints)

	/*LOCALT*/
	portsToNodeNames := util.BuildPortsToNodeNamesMap(endpoints)
	hostname, err := nodeutil.GetHostname("")
	klog.V(0).Infof("LoadBalancerRR: nodename %s", hostname)
	klog.V(0).Infof("LoadBalancerRR: portsToEndpoints %s", portsToEndpoints)
	klog.V(0).Infof("LoadBalancerRR: portsToNodeNames %s", portsToNodeNames)
	if err != nil {
		klog.V(1).Infof("LoadBalancerRR: Couldn't determine hostname")
	}
	/*END*/

	lb.lock.Lock()
	defer lb.lock.Unlock()

	for portname := range portsToEndpoints {
		klog.V(0).Infof("LoadBalancerRR: portname %s", portname)
		svcPort := proxy.ServicePortName{NamespacedName: types.NamespacedName{Namespace: endpoints.Namespace, Name: endpoints.Name}, Port: portname}
		newEndpoints := portsToEndpoints[portname]

		/*LOCALT*/
		nodenames := portsToNodeNames[portname]
		/*END*/
		klog.V(0).Infof("LoadBalancerRR: SERVICE", lb.services)
		klog.V(0).Infof("LoadBalancerRR: SERVICE", lb.services[svcPort])
		state, exists := lb.services[svcPort]
		klog.V(0).Infof("LoadBalancerRR: state", state)
		klog.V(0).Infof("LoadBalancerRR: SERVICE", lb.services[svcPort])
		if !exists || state == nil || len(newEndpoints) > 0 {

			klog.V(0).Infof("LoadBalancerRR: Setting endpoints for %s to %+v", svcPort, newEndpoints)
			// OnEndpointsAdd can be called without NewService being called externally.
			// To be safe we will call it here.  A new service will only be created
			// if one does not already exist.
			state = lb.newServiceInternal(svcPort, v1.ServiceAffinity(""), 0)

			//state.endpoints = util.ShuffleStrings(newEndpoints) //origina

			/*LOCALT*/
			state.localendpoints = nil
			state.endpoints = nil
			for i := range newEndpoints { ////////////////////EDIT DK nay

				if nodenames[i] == hostname {
					klog.V(0).Infof(" \n APPEND TOP - state.localendpoints >>> -->", state.localendpoints)
					state.localendpoints = append(state.localendpoints, newEndpoints[i])
					klog.V(0).Infof(" \n APPEND DOWN - state.localendpoints >>> -->", state.localendpoints)
				} else{
					state.endpoints = append(state.endpoints, newEndpoints[i])
				}
				//seltf check
				klog.V(0).Infof("OnEndpointsAdd: <<< iiiiiiiiiii >>> -->", i)
				klog.V(0).Infof("OnEndpointsAdd: <<< ADD - state.localendpoints >>> -->", state.localendpoints)
				klog.V(0).Infof("OnEndpointsAdd: <<< ADD - state.endpoints >>> -->", state.endpoints)
				klog.V(0).Infof("OnEndpointsAdd: <<< ADD - newEndpoints >>> -->", newEndpoints)
				klog.V(0).Infof("OnEndpointsAdd: <<< ADD - 	LEN(newEndpoints) >>> -->", len(newEndpoints))
				klog.V(0).Infof("OnEndpointsAdd: <<< ADD - nodenames[i] >>> -->", nodenames[i])
				klog.V(0).Infof("OnEndpointsAdd: <<< ADD - hostname >>> -->", hostname)
				klog.V(0).Infof("\n")
			}
			klog.V(0).Infof("LOCAL OnEndpointsAdd: service %s local endpoint %+v and other endpoint %+v", portname,state.localendpoints,state.endpoints)


			/*END*/
			// Reset the round-robin index.
			state.index = 0
			/*LOCALT*/
			state.localindex = 0
			/*END*/
		}
	}
}

func (lb *LoadBalancerRR) OnEndpointsUpdate(oldEndpoints, endpoints *v1.Endpoints) {
	portsToEndpoints := util.BuildPortsToEndpointsMap(endpoints)
	oldPortsToEndpoints := util.BuildPortsToEndpointsMap(oldEndpoints)
	registeredEndpoints := make(map[proxy.ServicePortName]bool)

	/*LOCALT*/
	portsToNodeNames := util.BuildPortsToNodeNamesMap(endpoints)
	hostname, err := nodeutil.GetHostname("")
	if err != nil {
		klog.V(1).Infof("LoadBalancerRR: Couldn't determine hostname")
	}
	/*END*/

	lb.lock.Lock()
	defer lb.lock.Unlock()

	for portname := range portsToEndpoints {
		svcPort := proxy.ServicePortName{NamespacedName: types.NamespacedName{Namespace: endpoints.Namespace, Name: endpoints.Name}, Port: portname}
		newEndpoints := portsToEndpoints[portname]
		nodenames := portsToNodeNames[portname] //LOCALY
		state, exists := lb.services[svcPort]

		curEndpoints := []string{}
		if state != nil {
			curEndpoints = state.endpoints
		}

		if !exists || state == nil || len(curEndpoints) != len(newEndpoints) || !slicesEquiv(slice.CopyStrings(curEndpoints), newEndpoints) {
			klog.V(0).Infof("LoadBalancerRR: Setting endpoints for %s to %+v", svcPort, newEndpoints)
			lb.removeStaleAffinity(svcPort, newEndpoints)
			// OnEndpointsUpdate can be called without NewService being called externally.
			// To be safe we will call it here.  A new service will only be created
			// if one does not already exist.  The affinity will be updated
			// later, once NewService is called.
			state = lb.newServiceInternal(svcPort, v1.ServiceAffinity(""), 0)

			state.endpoints = util.ShuffleStrings(newEndpoints) // original

			/**LOCALY**/
			state.localendpoints = nil
			state.endpoints = nil
			for i := range newEndpoints {
				if nodenames[i] == hostname {
					klog.V(0).Infof(" \n APPEND TOP - state.localendpoints >>> -->", state.localendpoints)
					state.localendpoints = append(state.localendpoints, newEndpoints[i])
					klog.V(0).Infof(" \n APPEND DOWN - state.localendpoints >>> -->", state.localendpoints)
				} else {
					state.endpoints = append(state.endpoints, newEndpoints[i])
				}
				//seltf check
				klog.V(0).Infof("LoadBalancerRR: <<< iiiiiiiiiii >>> -->", i)
				klog.V(0).Infof("LoadBalancerRR: <<< ADD - state.localendpoints >>> -->", state.localendpoints)
				klog.V(0).Infof("LoadBalancerRR: <<< ADD - state.endpoints >>> -->", state.endpoints)
				klog.V(0).Infof("LoadBalancerRR: <<< ADD - newEndpoints >>> -->", newEndpoints)
				klog.V(0).Infof("LoadBalancerRR: <<< ADD - 	LEN(newEndpoints) >>> -->", len(newEndpoints))
				klog.V(0).Infof("LoadBalancerRR: <<< ADD - nodenames[i] >>> -->", nodenames[i])
				klog.V(0).Infof("LoadBalancerRR: <<< ADD - hostname >>> -->", hostname)
				klog.V(0).Infof("\n")
			}
			klog.V(0).Infof("LoadBalancerRR: service %s LOCAL endpoint %+v and other endpoint %+v", portname,state.localendpoints,state.endpoints)
			/*END*/

			// Reset the round-robin index.
			state.index = 0
			state.localindex = 0
		}
		registeredEndpoints[svcPort] = true
	}

	// Now remove all endpoints missing from the update.
	for portname := range oldPortsToEndpoints {
		svcPort := proxy.ServicePortName{NamespacedName: types.NamespacedName{Namespace: oldEndpoints.Namespace, Name: oldEndpoints.Name}, Port: portname}
		if _, exists := registeredEndpoints[svcPort]; !exists {
			lb.resetService(svcPort)
		}
	}
}

func (lb *LoadBalancerRR) resetService(svcPort proxy.ServicePortName) {
	// If the service is still around, reset but don't delete.
	if state, ok := lb.services[svcPort]; ok {
		if len(state.endpoints) > 0 {
			klog.V(2).Infof("LoadBalancerRR: Removing endpoints for %s", svcPort)
			state.endpoints = []string{}
		}
		state.index = 0
		state.affinity.affinityMap = map[string]*affinityState{}
	}
}

func (lb *LoadBalancerRR) OnEndpointsDelete(endpoints *v1.Endpoints) {
	portsToEndpoints := util.BuildPortsToEndpointsMap(endpoints)

	lb.lock.Lock()
	defer lb.lock.Unlock()

	for portname := range portsToEndpoints {
		svcPort := proxy.ServicePortName{NamespacedName: types.NamespacedName{Namespace: endpoints.Namespace, Name: endpoints.Name}, Port: portname}
		lb.resetService(svcPort)
	}
}

func (lb *LoadBalancerRR) OnEndpointsSynced() {
}

// Tests whether two slices are equivalent.  This sorts both slices in-place.
func slicesEquiv(lhs, rhs []string) bool {
	if len(lhs) != len(rhs) {
		return false
	}
	if reflect.DeepEqual(slice.SortStrings(lhs), slice.SortStrings(rhs)) {
		return true
	}
	return false
}

func (lb *LoadBalancerRR) CleanupStaleStickySessions(svcPort proxy.ServicePortName) {
	lb.lock.Lock()
	defer lb.lock.Unlock()

	state, exists := lb.services[svcPort]
	if !exists {
		return
	}
	for ip, affinity := range state.affinity.affinityMap {
		if int(time.Since(affinity.lastUsed).Seconds()) >= state.affinity.ttlSeconds {
			klog.V(4).Infof("Removing client %s from affinityMap for service %q", affinity.clientIP, svcPort)
			delete(state.affinity.affinityMap, ip)
		}
	}
}
