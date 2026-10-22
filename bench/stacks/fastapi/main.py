"""The notes API as a typical FastAPI app: SQLAlchemy on Postgres, served by uvicorn."""
import os
from datetime import datetime

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel
from sqlalchemy import DateTime, Text, create_engine, func, select, text
from sqlalchemy.orm import DeclarativeBase, Mapped, Session, mapped_column, sessionmaker

engine = create_engine(os.environ["DATABASE_URL"])
SessionLocal = sessionmaker(engine)


class Base(DeclarativeBase):
    pass


class Note(Base):
    __tablename__ = "notes"
    id: Mapped[int] = mapped_column(primary_key=True)
    body: Mapped[str] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())


Base.metadata.create_all(engine)


class NoteIn(BaseModel):
    body: str


class NoteOut(BaseModel):
    id: int
    body: str
    createdAt: datetime


def get_db():
    with SessionLocal() as db:
        yield db


app = FastAPI()


@app.get("/healthz")
def healthz(db: Session = Depends(get_db)):
    db.execute(text("SELECT 1"))
    return "ok"


@app.get("/api/notes", response_model=list[NoteOut])
def list_notes(db: Session = Depends(get_db)):
    notes = db.scalars(select(Note).order_by(Note.id.desc()).limit(100))
    return [NoteOut(id=n.id, body=n.body, createdAt=n.created_at) for n in notes]


@app.post("/api/notes", response_model=NoteOut, status_code=201)
def create_note(data: NoteIn, db: Session = Depends(get_db)):
    body = data.body.strip()
    if not body:
        raise HTTPException(400, "body is required")
    note = Note(body=body)
    db.add(note)
    db.commit()
    db.refresh(note)
    return NoteOut(id=note.id, body=note.body, createdAt=note.created_at)
