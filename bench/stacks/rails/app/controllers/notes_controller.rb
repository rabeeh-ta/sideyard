# The notes API as a typical Rails app: an API-only app with Active Record on Postgres.
class NotesController < ApplicationController
  def index
    render json: Note.order(id: :desc).limit(100).map { |n| as_json_note(n) }
  end

  def create
    body = params[:body].to_s.strip
    return render(plain: "body is required", status: :bad_request) if body.empty?

    render json: as_json_note(Note.create!(body: body)), status: :created
  end

  private

  def as_json_note(n)
    { id: n.id, body: n.body, createdAt: n.created_at }
  end
end
