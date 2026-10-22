Rails.application.routes.draw do
  get "healthz" => "health#show"
  get "api/notes" => "notes#index"
  post "api/notes" => "notes#create"
end
