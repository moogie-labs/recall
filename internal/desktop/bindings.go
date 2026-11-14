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
func (a *API) Move(input MoveInput) MoveReply             { return a.service.Move(input) }
func (a *API) Search(input SearchInput) SearchReply       { return a.service.Search(input) }
func (a *API) Refresh(session string) TreeReply           { return a.service.Refresh(session) }
func (a *API) SetupStatus(session string) SetupStatus     { return a.service.SetupStatus(session) }
func (a *API) RetrySetup(session string) OperationReply   { return a.service.RetrySetup(session) }

func (a *API) AppInfo() AppInfo { return a.service.AppInfo() }
func (a *API) SetCheckForUpdates(enabled bool) OperationReply {
	return a.service.SetCheckForUpdates(enabled)
}
func (a *API) CheckForUpdate(manual bool) UpdateReply { return a.service.CheckForUpdate(manual) }
func (a *API) StartUpgrade() OperationReply           { return a.service.StartUpgrade() }

func (a *API) SaveImage(input SaveImageInput) SaveImageReply { return a.service.SaveImage(input) }
func (a *API) Attachments(session, id string) AttachmentsReply {
	return a.service.Attachments(session, id)
}
