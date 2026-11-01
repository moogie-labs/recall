package desktop

// API is the renderer's explicit allowlist. Lifecycle hooks and callback
// registration belong to the native host and are intentionally not bound.
type API struct{ service *Service }

func Bindings(service *Service) *API                      { return &API{service} }
func (a *API) OpenCollection(root string) CollectionReply { return a.service.OpenCollection(root) }
func (a *API) OpenDocument(session, id string) DocumentReply {
	return a.service.OpenDocument(session, id)
}
func (a *API) SaveBody(input SaveBodyInput) SaveBodyReply { return a.service.SaveBody(input) }
func (a *API) Tree(session string) TreeReply              { return a.service.Tree(session) }
func (a *API) Create(input CreateInput) CreateReply       { return a.service.Create(input) }
func (a *API) Delete(input DeleteInput) OperationReply    { return a.service.Delete(input) }
func (a *API) Search(input SearchInput) SearchReply       { return a.service.Search(input) }
func (a *API) Refresh(session string) TreeReply           { return a.service.Refresh(session) }
func (a *API) SetupStatus(session string) SetupStatus     { return a.service.SetupStatus(session) }
func (a *API) RetrySetup(session string) OperationReply   { return a.service.RetrySetup(session) }
