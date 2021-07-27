package informer

import (
	kapi "k8s.io/api/core/v1"
)

type ServiceEventHandler interface {
	AddService(*kapi.Service)
	DeleteService(*kapi.Service)
	UpdateService(old, new *kapi.Service)
	SyncServices([]interface{})
}

type EndpointsEventHandler interface {
	AddEndpoints(*kapi.Endpoints, *kapi.Service)
	DeleteEndpoints(*kapi.Endpoints, *kapi.Service)
	UpdateEndpoints(old, new *kapi.Endpoints)
}

type ServiceAndEndpointsEventHandler interface {
	ServiceEventHandler
	EndpointsEventHandler
}
