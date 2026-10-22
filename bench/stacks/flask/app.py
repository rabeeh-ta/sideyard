"""The notes API as a typical Flask app: Flask-SQLAlchemy on Postgres, served by gunicorn."""
import os

from flask import Flask, jsonify, request
from flask_sqlalchemy import SQLAlchemy
from sqlalchemy import text

app = Flask(__name__)
app.config["SQLALCHEMY_DATABASE_URI"] = os.environ["DATABASE_URL"]
db = SQLAlchemy(app)


class Note(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    body = db.Column(db.Text, nullable=False)
    created_at = db.Column(db.DateTime(timezone=True), nullable=False, server_default=db.func.now())

    def to_dict(self):
        return {"id": self.id, "body": self.body, "createdAt": self.created_at.isoformat()}


with app.app_context():
    db.create_all()


@app.get("/healthz")
def healthz():
    db.session.execute(text("SELECT 1"))
    return "ok"


@app.get("/api/notes")
def list_notes():
    notes = db.session.scalars(db.select(Note).order_by(Note.id.desc()).limit(100))
    return jsonify([n.to_dict() for n in notes])


@app.post("/api/notes")
def create_note():
    body = ((request.get_json(silent=True) or {}).get("body") or "").strip()
    if not body:
        return "body is required", 400
    note = Note(body=body)
    db.session.add(note)
    db.session.commit()
    return jsonify(note.to_dict()), 201
