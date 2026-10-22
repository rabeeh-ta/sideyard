from django.db import models


class Note(models.Model):
    body = models.TextField()
    created_at = models.DateTimeField(auto_now_add=True)
