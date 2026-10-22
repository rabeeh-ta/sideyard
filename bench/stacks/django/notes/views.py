import json

from django.db import connection
from django.http import HttpResponse, JsonResponse
from django.views.decorators.csrf import csrf_exempt

from .models import Note


def to_dict(n):
    return {"id": n.id, "body": n.body, "createdAt": n.created_at.isoformat()}


def healthz(request):
    with connection.cursor() as cursor:
        cursor.execute("SELECT 1")
    return HttpResponse("ok")


@csrf_exempt
def notes(request):
    if request.method == "POST":
        try:
            body = (json.loads(request.body).get("body") or "").strip()
        except (ValueError, AttributeError):
            body = ""
        if not body:
            return HttpResponse("body is required", status=400)
        return JsonResponse(to_dict(Note.objects.create(body=body)), status=201)
    return JsonResponse([to_dict(n) for n in Note.objects.order_by("-id")[:100]], safe=False)
